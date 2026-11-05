package retrieval

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hydra/mcp-hub/internal/search/client"
	"github.com/hydra/mcp-hub/internal/search/corpus"
	"github.com/hydra/mcp-hub/internal/store/embeddings"
	"go.uber.org/zap"
)

// Rebuild 对当前代做增量（重）嵌入：corpus_hash 没变的跳过，单批失败保留旧向量并继续。
// 同一时刻只有一个重建在跑，已有重建时立刻返回 RebuildSkipped（不排队，避免拖长探活周期）。
// ctx 取消会取消在途批次并以失败收场，不会假装成功。
func (m *EmbedManager) Rebuild(ctx context.Context) RebuildStatus {
	m.mu.Lock()
	if m.status.State == RebuildRunning {
		st := m.status
		m.mu.Unlock()
		// 只改本次调用的返回值，管理器自己的状态仍是 running。
		st.State = RebuildSkipped
		st.Ready = false
		st.Message = "已有向量重建在进行中，本次调用已跳过"
		if st.Total > 0 {
			st.Message = fmt.Sprintf("%s（当前进度 %d/%d）", st.Message, st.Done, st.Total)
		}
		return st
	}
	e := m.embedder
	cfg := m.cfg
	m.status = RebuildStatus{State: RebuildRunning, UpdatedAt: time.Now().UnixMilli()}
	m.mu.Unlock()

	return m.buildGeneration(ctx, cfg, e)
}

// buildGeneration 构建某一代的向量：增量判定 → 批量 embed → 写库。
// 写库只触碰目标代的行（upsert 键含代身份），与其它代的行互不干扰。
func (m *EmbedManager) buildGeneration(ctx context.Context, cfg EmbedConfig, e *client.Embedder) RebuildStatus {
	if e == nil {
		return m.finish(RebuildFailed, 0, 0, 0, 0, 0,
			"未配置 embedding 服务（缺 MCPHUB_EMBED_API_KEY）")
	}

	gen := genOf(cfg)

	docs, badSchema, err := collectToolDocs(ctx, m.store, cfg.CorpusFields)
	if err != nil {
		return m.finish(RebuildFailed, 0, 0, 0, 0, 0, fmt.Sprintf("读取工具语料失败: %v", err))
	}
	if badSchema > 0 {
		m.log.Warn("部分工具的 inputSchema 无法解析，其语料缺少参数线索",
			zap.Int("bad", badSchema), zap.Int("total", len(docs)))
	}

	existing, err := m.store.Embeddings.List(ctx)
	if err != nil {
		return m.finish(RebuildFailed, 0, 0, 0, 0, 0, fmt.Sprintf("读取已有向量失败: %v", err))
	}
	// 只看本代的行：别的代有自己的 hash，混进来会误判全部要重嵌。
	hashOf := make(map[string]string, len(existing))
	for _, x := range existing {
		if x.Model == gen.Model && x.Dims == gen.Dims && x.Spec == gen.Spec {
			hashOf[x.ToolID] = x.CorpusHash
		}
	}

	live := make(map[string]bool, len(docs))
	pending := make([]toolDoc, 0, len(docs))
	for _, d := range docs {
		if strings.TrimSpace(d.Corpus) == "" {
			continue // 无语料不参与 embed：空串的向量没有检索意义
		}
		live[d.ToolID] = true
		if hashOf[d.ToolID] != corpus.Hash(d.Corpus) {
			pending = append(pending, d)
		}
	}

	// 清掉已不存在（或语料变空）的工具的旧向量：删该工具所有代的行，
	// 工具没了，任何代下的向量都是垃圾。
	stale := make([]string, 0)
	for id := range hashOf {
		if !live[id] {
			stale = append(stale, id)
		}
	}
	if err := m.store.Embeddings.Delete(ctx, stale); err != nil {
		m.log.Warn("清理失效向量失败", zap.Error(err))
	}

	m.progress(len(pending), 0, 0, fmt.Sprintf("共 %d 个工具有语料，其中 %d 个需要重新向量化", len(live), len(pending)))

	// 每批是一个独立作业：最多 rebuildConcurrency 个并行，单批失败只损失
	// 自己（保留旧向量，下轮增量重嵌），不拖垮整次构建。批内并发由
	// Embedder 自己保证，这里让批与批之间也并行起来。
	const rebuildConcurrency = 4

	batch := batchOrDefault(cfg.Batch)
	var embeddedN, failedN atomic.Int64
	var wg sync.WaitGroup
	sem := make(chan struct{}, rebuildConcurrency)
	for start := 0; start < len(pending); start += batch {
		job := pending[start:min(start+batch, len(pending))]
		sem <- struct{}{}
		wg.Add(1)
		go func(job []toolDoc) {
			defer wg.Done()
			defer func() { <-sem }()

			texts := make([]string, 0, len(job))
			for _, d := range job {
				texts = append(texts, d.Corpus)
			}
			vecs, err := e.Embed(ctx, texts)
			if err != nil {
				failedN.Add(int64(len(job)))
				m.log.Warn("一批 embedding 失败，保留旧向量",
					zap.Int("count", len(job)), zap.Error(err))
				m.progress(len(pending), int(embeddedN.Load()), int(failedN.Load()),
					fmt.Sprintf("已向量化 %d/%d（%d 失败）", embeddedN.Load()+failedN.Load(), len(pending), failedN.Load()))
				return
			}

			es := make([]embeddings.Embedding, 0, len(vecs))
			for i, v := range vecs {
				d := job[i]
				es = append(es, embeddings.Embedding{
					ToolID:     d.ToolID,
					Model:      cfg.Model,
					Dims:       cfg.Dimensions,
					Spec:       gen.Spec,
					Vector:     v,
					CorpusHash: corpus.Hash(d.Corpus),
					CreatedAt:  time.Now(),
				})
			}
			if err := m.store.Embeddings.Save(ctx, es); err != nil {
				failedN.Add(int64(len(es)))
				m.log.Warn("向量写库失败，保留旧向量", zap.Int("count", len(es)), zap.Error(err))
				m.progress(len(pending), int(embeddedN.Load()), int(failedN.Load()),
					fmt.Sprintf("已向量化 %d/%d（%d 失败）", embeddedN.Load()+failedN.Load(), len(pending), failedN.Load()))
				return
			}
			embeddedN.Add(int64(len(es)))
			m.progress(len(pending), int(embeddedN.Load()), int(failedN.Load()),
				fmt.Sprintf("已向量化 %d/%d", embeddedN.Load()+failedN.Load(), len(pending)))
		}(job)
	}
	wg.Wait()
	embedded, failed := int(embeddedN.Load()), int(failedN.Load())

	// 就绪判定：本代下全部有语料的工具都有向量。构建期间新增的工具
	// 此刻还没有向量 → 未就绪，下一轮 45s 增量会补上。
	covered, liveCount := m.coverageOf(ctx, gen, docs)
	state := RebuildDone
	if failed > 0 {
		state = RebuildFailed
	}
	msg := fmt.Sprintf("重建完成：新向量化 %d 个工具", embedded)
	if failed > 0 {
		msg = fmt.Sprintf("重建完成：%d 成功，%d 失败（失败项保留旧向量）", embedded, failed)
	}

	st := m.finish(state, len(pending), embedded, failed, covered, liveCount, msg)
	// 完全就绪才激活这一代供检索：构建中途（或失败）的代不进向量分支，
	// 代切换因此是原子的——切换窗口里检索一直用上一代就绪的向量。
	if st.Ready {
		m.mu.Lock()
		m.activeGen = &gen
		m.mu.Unlock()
	}
	return st
}

// coverageOf 统计某一代对"有语料的工具"的覆盖数。
func (m *EmbedManager) coverageOf(ctx context.Context, gen embeddings.Generation, docs []toolDoc) (covered, live int) {
	existing, err := m.store.Embeddings.List(ctx)
	if err != nil {
		return 0, 0
	}
	have := make(map[string]bool, len(existing))
	for _, x := range existing {
		if x.Model == gen.Model && x.Dims == gen.Dims && x.Spec == gen.Spec {
			have[x.ToolID] = true
		}
	}
	for _, d := range docs {
		if strings.TrimSpace(d.Corpus) == "" {
			continue
		}
		live++
		if have[d.ToolID] {
			covered++
		}
	}
	return covered, live
}

// Reconcile 是启动时的一次性对齐：补齐当前代（由进程配置算出）的向量，
// 补全后清掉其它代的遗留行。构建期间检索只用上一代就绪的向量（没有就降级 bm25）。
// 清理只在完全就绪时做：旧代行不参与检索，留着便于改回旧配置后直接就绪。
func (m *EmbedManager) Reconcile(ctx context.Context) RebuildStatus {
	st := m.Rebuild(ctx)
	if st.State == RebuildSkipped {
		// 已有重建在跑：对账这次什么也没做。不能按"当前代没补齐"下结论——
		// 在跑的那次结束后状态会自己落定，当前代到底就绪没有要等它。
		m.log.Info("向量对账跳过：已有向量重建在进行中")
		return st
	}
	staleRows := m.otherGenerationRows(ctx)
	if st.Ready {
		if staleRows > 0 {
			m.log.Info("当前代向量已就绪，清理旧代遗留",
				zap.Int("rows", staleRows), zap.String("model", m.Config().Model))
		}
		m.dropOtherGenerations(ctx)
		return st
	}
	if staleRows > 0 {
		m.log.Warn("当前代向量尚未补齐，检索暂时降级为 bm25",
			zap.String("message", st.Message), zap.Int("keptRows", staleRows))
	}
	return st
}

// otherGenerationRows 统计不属于当前代的向量行数（配置改动后留下的遗留）。
func (m *EmbedManager) otherGenerationRows(ctx context.Context) int {
	gen := genOf(m.Config())
	all, err := m.store.Embeddings.List(ctx)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range all {
		if e.Model != gen.Model || e.Dims != gen.Dims || e.Spec != gen.Spec {
			n++
		}
	}
	return n
}

// dropOtherGenerations 删掉当前代之外的全部向量行。
// 只在 Reconcile 判定当前代已就绪后调用：没有回滚接口，旧代向量
// 留着没用，只占存储。
func (m *EmbedManager) dropOtherGenerations(ctx context.Context) {
	if err := m.store.Embeddings.DeleteStaleGenerations(ctx, genOf(m.Config())); err != nil {
		m.log.Warn("清理旧代向量失败", zap.Error(err))
	}
}

// progress 更新运行中进度（不清空状态）。
func (m *EmbedManager) progress(total, done, failed int, msg string) {
	m.mu.Lock()
	m.status.Total = total
	m.status.Done = done
	m.status.Failed = failed
	if msg != "" {
		m.status.Message = msg
	}
	m.status.UpdatedAt = time.Now().UnixMilli()
	m.mu.Unlock()
}

// finish 落定终态并返回。ready = 无失败 且 全覆盖。
func (m *EmbedManager) finish(state RebuildState, total, done, failed, covered, live int, msg string) RebuildStatus {
	st := RebuildStatus{
		State:     state,
		Total:     total,
		Done:      done,
		Failed:    failed,
		Covered:   covered,
		Ready:     state == RebuildDone && failed == 0 && covered == live,
		Message:   msg,
		UpdatedAt: time.Now().UnixMilli(),
	}
	m.mu.Lock()
	m.status = st
	m.mu.Unlock()
	return st
}
