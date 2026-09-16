package retrieval

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hydra/mcp-hub/internal/search/client"
	"github.com/hydra/mcp-hub/internal/search/corpus"
	"github.com/hydra/mcp-hub/internal/store/embeddings"
	"go.uber.org/zap"
)

// Rebuild 对当前代做增量（重）嵌入（PRD §4.7.5）。
//
// corpus_hash 没变的跳过，成本与变更量成正比；单批失败保留旧向量并继续，
// 重建失败不抹掉已有能力。同一时刻只有一个重建在跑，重复调用直接返回进度。
// 探活循环每轮调用它：补新工具向量、重嵌变更语料、清理死工具向量。
func (m *EmbedManager) Rebuild(ctx context.Context) RebuildStatus {
	m.mu.Lock()
	if m.status.State == RebuildRunning {
		st := m.status
		m.mu.Unlock()
		return st
	}
	e := m.embedder
	cfg := m.cfg
	m.status = RebuildStatus{State: RebuildRunning, UpdatedAt: time.Now().UnixMilli()}
	m.mu.Unlock()

	return m.buildGeneration(ctx, cfg, e)
}

// buildGeneration 构建某一代的向量：增量判定 → 批量 embed → 写库。
//
// 写库只触碰目标代的行（upsert 键含代身份），因此与其它代的行互不干扰。
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

	embedded, failed := 0, 0
	batch := batchOrDefault(cfg.Batch)
	for start := 0; start < len(pending); start += batch {
		end := start + batch
		if end > len(pending) {
			end = len(pending)
		}
		texts := make([]string, 0, end-start)
		for _, d := range pending[start:end] {
			texts = append(texts, d.Corpus)
		}

		vecs, err := e.Embed(ctx, texts)
		if err != nil {
			failed += end - start
			m.log.Warn("一批 embedding 失败，保留旧向量",
				zap.Int("count", end-start), zap.Error(err))
			m.progress(len(pending), embedded, failed, fmt.Sprintf("已向量化 %d/%d（%d 批失败）", end, len(pending), failed))
			continue
		}

		es := make([]embeddings.Embedding, 0, len(vecs))
		for i, v := range vecs {
			d := pending[start+i]
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
			failed += len(es)
			m.log.Warn("向量写库失败，保留旧向量", zap.Int("count", len(es)), zap.Error(err))
			continue
		}
		embedded += len(es)
		m.progress(len(pending), embedded, failed, fmt.Sprintf("已向量化 %d/%d", end, len(pending)))
	}

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

	return m.finish(state, len(pending), embedded, failed, covered, liveCount, msg)
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

// Reconcile 是启动时的一次性对齐：把当前代（由进程配置算出）的向量补齐，
// 补全后清掉其它代的遗留行。换了 MCPHUB_EMBED_MODEL / EMBED_DIMS 后重启
// 走的就是这条路。构建期间向量一路为空（hybrid 自动退回 keyword+bm25），
// 完成即刻恢复，不需要重启或人工干预。
//
// 清理只在完全就绪（无失败 + 全覆盖）时做：embed 服务暂时打不通时旧代的
// 行还留着，它们不参与检索，但那是唯一的退路，没把握时不该删。
func (m *EmbedManager) Reconcile(ctx context.Context) RebuildStatus {
	st := m.Rebuild(ctx)
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
		m.log.Warn("当前代向量尚未补齐，检索暂时降级为 keyword/bm25",
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
