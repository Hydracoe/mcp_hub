package retrieval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/hydra/mcp-hub/internal/search/corpus"
	"github.com/hydra/mcp-hub/internal/search/lexical"
	"github.com/hydra/mcp-hub/internal/search/rank"
	"github.com/hydra/mcp-hub/internal/store"
	"go.uber.org/zap"
)

// 检索模式（PRD §4.7.4）。
const (
	searchModeKeyword = "keyword"
	searchModeBM25    = "bm25"
	searchModeHybrid  = "hybrid"
)

// ToolHit 是一次工具检索的结果（PRD §4.7.4）。
// 树式发现下默认是"薄结果"：不带 inputSchema，AI 圈定候选后再用
// hub__tool 按名取完整定义——单次搜索的上下文开销与工具总数无关。
type ToolHit struct {
	Exposed     string          `json:"exposedName"`
	Name        string          `json:"name"`
	ServerKey   string          `json:"serverKey"`
	Description string          `json:"description,omitempty"`
	CategoryID  string          `json:"categoryId,omitempty"`
	Category    string          `json:"category,omitempty"`
	Tags        []string        `json:"tags,omitempty"`
	Score       float64         `json:"score"`
	Schema      json.RawMessage `json:"inputSchema,omitempty"` // 仅 WithSchema 时填充
}

// SearchQuery 是工具检索的输入。
type SearchQuery struct {
	Query      string
	Mode       string // keyword | bm25 | hybrid；空按 hybrid
	Category   string // 分类 id 或名称
	Server     string // serverKey
	Tag        string
	K          int  // 0 按默认 5
	WithSchema bool // 结果内联完整 inputSchema（跳过 hub__tool 一步的退路）
}

// FacetCount 是一个 facet 取值及其在本次结果中的命中次数。
type FacetCount struct {
	Name string `json:"name"`
	Hits int    `json:"hits"`
}

// Facets 是结果的命中分布，告诉 AI 下一步该用哪个过滤器收窄。
// 搜偏了不必靠瞎猜换词：看到"3 个命中都在 amap"，自然知道带
// server=amap 重搜。
type Facets struct {
	Categories []FacetCount `json:"categories,omitempty"`
	Servers    []FacetCount `json:"servers,omitempty"`
}

// FacetsOf 从结果列表汇总 facet 分布，按命中数降序、同名按字母序。
func FacetsOf(hits []ToolHit) Facets {
	cats := map[string]int{}
	srvs := map[string]int{}
	for _, h := range hits {
		if h.Category != "" {
			cats[h.Category]++
		} else if h.CategoryID != "" {
			cats[h.CategoryID]++
		}
		if h.ServerKey != "" {
			srvs[h.ServerKey]++
		}
	}
	return Facets{
		Categories: facetList(cats),
		Servers:    facetList(srvs),
	}
}

func facetList(m map[string]int) []FacetCount {
	out := make([]FacetCount, 0, len(m))
	for name, hits := range m {
		out = append(out, FacetCount{Name: name, Hits: hits})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Hits != out[j].Hits {
			return out[i].Hits > out[j].Hits
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// idScore 是一条"工具 id + 分数"的中间结果。
// RRF 融合与 rerank 全程用 tool id 流转，最后才映射回展示数据——
// 中途换成对外名会在"同名工具跨 server"的边界上串线。
type idScore struct {
	id    string
	score float64
}

// SearchTools 按三种模式检索工具（PRD §4.7.4）。
//
// 降级阶梯，每一级都保持可用：rerank 挂 → 保持融合顺序；embed 挂 →
// 只用 bm25；无 pgvector → keyword 与 bm25 融合。辅助能力挂掉不该让
// 检索整体报错——搜不到比报错更接近可用。
func (e *Engine) Search(ctx context.Context, q SearchQuery) ([]ToolHit, error) {
	e.mu.Lock()
	ix := e.ix
	e.mu.Unlock()
	if ix == nil {
		return []ToolHit{}, nil
	}

	mode := strings.ToLower(strings.TrimSpace(q.Mode))
	if mode == "" {
		mode = searchModeHybrid
	}
	switch mode {
	case searchModeKeyword, searchModeBM25, searchModeHybrid:
	default:
		return nil, fmt.Errorf("不支持的检索模式 %q（可选 keyword / bm25 / hybrid）", q.Mode)
	}

	query := strings.TrimSpace(q.Query)
	if query == "" {
		return nil, errors.New("query 不能为空")
	}

	k := q.K
	if k <= 0 {
		// 树式发现下默认 5：搜索给候选方向而非穷举，召回靠 facets 收窄，
		// schema 靠 hub__tool 按需取。
		k = 5
	}
	if k > 50 {
		k = 50
	}

	// 过滤器在打分之后应用：BM25 的 IDF 要在完整语料上算才准，先过滤
	// 会让"恰好只剩一个含该词的工具"拿到虚高的相关度。
	catID, _ := ResolveCategory(ctx, e.st, q.Category)
	match := func(d toolDoc) bool {
		if q.Server != "" && !strings.EqualFold(d.ServerKey, q.Server) {
			return false
		}
		if q.Tag != "" {
			ok := false
			for _, t := range d.Tags {
				if strings.EqualFold(t, q.Tag) {
					ok = true
					break
				}
			}
			if !ok {
				return false
			}
		}
		if q.Category != "" && d.CategoryID != catID && !strings.EqualFold(d.Category, q.Category) {
			return false
		}
		return true
	}

	switch mode {
	case searchModeKeyword:
		return hitsFromIDs(ix, keywordBranch(ix, query, k, match), q.WithSchema), nil

	case searchModeBM25:
		return hitsFromIDs(ix, e.bm25Branch(ix, query, k, match), q.WithSchema), nil

	default: // hybrid
		lists := [][]string{idsOf(e.bm25Branch(ix, query, 50, match), 50)}

		em := e.em
		if em != nil {
			vecIDs, err := e.vectorBranch(ctx, ix, em, query, match)
			if err == nil {
				lists = append(lists, vecIDs)
			} else {
				// 向量一路挂掉（含未装 pgvector 扩展）：退回
				// keyword+bm25 融合，检索整体保持可用。
				e.log.Warn("向量检索分支失败，退回 keyword+bm25", zap.Error(err))
				lists = append(lists, idsOf(keywordBranch(ix, query, 50, match), 50))
			}
		} else {
			lists = [][]string{
				idsOf(keywordBranch(ix, query, 50, match), 50),
				idsOf(e.bm25Branch(ix, query, 50, match), 50),
			}
		}

		fused := rank.RRF(rank.DefaultK, lists)
		if len(fused) > 20 {
			fused = fused[:20]
		}
		out := make([]idScore, 0, len(fused))
		for _, h := range fused {
			if _, ok := ix.byID[h.ID]; ok {
				out = append(out, idScore{id: h.ID, score: h.Score})
			}
		}

		// rerank 是锦上添花：它挂了保持融合顺序，不报错。
		// 返回空结果同样按"没排成"处理——用它覆盖 out 会把整页结果清空。
		if em != nil && em.Reranker() != nil && len(out) > 1 {
			texts := make([]string, 0, len(out))
			for _, sc := range out {
				texts = append(texts, ix.docs[ix.byID[sc.id]].Corpus)
			}
			ranked, err := em.Reranker().Rerank(ctx, query, texts, k)
			switch {
			case err != nil:
				e.log.Warn("rerank 失败，保持融合顺序", zap.Error(err))
			case len(ranked) == 0:
				e.log.Warn("rerank 未返回任何有效结果，保持融合顺序")
			default:
				// 客户端已保证下标合法且按分数降序，这里直接映射。
				reordered := make([]idScore, 0, len(ranked))
				for _, r := range ranked {
					reordered = append(reordered, idScore{id: out[r.Index].id, score: r.Score})
				}
				out = reordered
			}
		}

		return hitsFromIDs(ix, out, q.WithSchema), nil
	}
}

// bm25Branch 跑 BM25 一路并应用过滤器，按分数降序返回 tool id。
func (e *Engine) bm25Branch(ix *searchIndex, query string, k int, match func(toolDoc) bool) []idScore {
	want := k*5 + 10
	if n := ix.bm25.Count(); n < want {
		want = n
	}
	out := make([]idScore, 0, k)
	for _, h := range ix.bm25.Search(lexical.Tokenize(query), want) {
		i, ok := ix.byID[h.ID]
		if !ok || !match(ix.docs[i]) {
			continue
		}
		out = append(out, idScore{id: h.ID, score: h.Score})
		if len(out) >= k {
			break
		}
	}
	return out
}

// keywordBranch 跑 keyword 粗筛一路（分数无意义，记 0）。
func keywordBranch(ix *searchIndex, query string, k int, match func(toolDoc) bool) []idScore {
	out := make([]idScore, 0, k)
	for _, id := range ix.bm25.KeywordHits(query) {
		i, ok := ix.byID[id]
		if !ok || !match(ix.docs[i]) {
			continue
		}
		out = append(out, idScore{id: id})
		if len(out) >= k {
			break
		}
	}
	return out
}

// vectorBranch 走 pgvector ANN 一路：嵌入查询 → 余弦距离排序 → 过滤。
func (e *Engine) vectorBranch(ctx context.Context, ix *searchIndex, em *EmbedManager, query string, match func(toolDoc) bool) ([]string, error) {
	vec, err := em.EmbedQuery(ctx, query)
	if err != nil {
		return nil, err
	}
	cfg := em.Config()
	// 只查当前代（model+dims+spec）的向量：旧代的向量还在表里，
	// 混进检索就是两套语义空间打架。
	ids, err := e.st.Embeddings.Search(ctx, cfg.Model, cfg.Dimensions, corpus.SpecString(cfg.CorpusFields), vec, 50)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(ids))
	for _, h := range ids {
		i, ok := ix.byID[h.ToolID]
		if !ok || !match(ix.docs[i]) {
			continue
		}
		out = append(out, h.ToolID)
	}
	return out, nil
}

func idsOf(list []idScore, n int) []string {
	if len(list) > n {
		list = list[:n]
	}
	out := make([]string, 0, len(list))
	for _, sc := range list {
		out = append(out, sc.id)
	}
	return out
}

func hitsFromIDs(ix *searchIndex, list []idScore, withSchema bool) []ToolHit {
	out := make([]ToolHit, 0, len(list))
	for _, sc := range list {
		i, ok := ix.byID[sc.id]
		if !ok {
			continue
		}
		out = append(out, hitFrom(ix.docs[i], sc.score, withSchema))
	}
	return out
}

func hitFrom(d toolDoc, score float64, withSchema bool) ToolHit {
	schema := json.RawMessage(nil)
	if withSchema {
		// WithSchema 是"一次直达"的退路：默认薄结果省上下文，
		// 明确要 schema 时才把这份 KB 级的开销塞进返回。
		schema = d.Schema
		if len(schema) == 0 || !json.Valid(schema) {
			schema = json.RawMessage(`{"type":"object"}`)
		}
	}
	return ToolHit{
		Exposed:     d.Exposed,
		Name:        d.Name,
		ServerKey:   d.ServerKey,
		Description: d.Description,
		CategoryID:  d.CategoryID,
		Category:    d.Category,
		Tags:        d.Tags,
		Score:       score,
		Schema:      schema,
	}
}

// ResolveCategory 把 id 或名称解析成分类 id；都不是时原样返回（过滤条件落空，返回空结果）。
func ResolveCategory(ctx context.Context, st *store.Store, nameOrID string) (string, error) {
	if nameOrID == "" {
		return "", nil
	}
	cats, err := st.Categories.List(ctx)
	if err != nil {
		return nameOrID, err
	}
	for _, c := range cats {
		if c.ID == nameOrID || strings.EqualFold(c.Name, nameOrID) {
			return c.ID, nil
		}
	}
	return nameOrID, nil
}
