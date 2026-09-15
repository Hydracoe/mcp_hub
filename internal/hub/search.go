package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/hydra/mcp-hub/internal/downstream"
	"github.com/hydra/mcp-hub/internal/search"
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
//
// 树式发现（PRD §4.7.6）下默认是"薄结果"：不带 inputSchema，
// AI 圈定候选后再用 hub__tool 按名取完整定义——每次搜索的上下文
// 开销从 KB 级降到百字节级，且与工具总数无关。
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
//
// 树式查找里 AI 搜偏了不该靠瞎猜换词：看到"3 个命中都在 amap"，
// 它自然知道带 server=amap 重搜。
type Facets struct {
	Categories []FacetCount `json:"categories,omitempty"`
	Servers    []FacetCount `json:"servers,omitempty"`
}

// FacetsOf 从结果列表汇总 facet 分布，按命中数降序、同名按字母序。
func FacetsOf(hits []ToolHit) Facets {
	cats := map[string]int{}
	servers := map[string]int{}
	for _, h := range hits {
		if h.Category != "" {
			cats[h.Category]++
		} else if h.CategoryID != "" {
			cats[h.CategoryID]++
		}
		if h.ServerKey != "" {
			servers[h.ServerKey]++
		}
	}
	return Facets{
		Categories: facetList(cats),
		Servers:    facetList(servers),
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

// toolDoc 是索引里的一个工具：语料、权重展开与展示元数据。
type toolDoc struct {
	ToolID      string
	ServerID    string
	Exposed     string
	Name        string
	ServerKey   string
	Description string
	CategoryID  string
	Category    string
	Tags        []string
	ServerDesc  string
	Schema      json.RawMessage
	Corpus      string // 按当前语料字段组装的文本，embed 与 rerank 共用
}

// searchIndex 是检索的内存快照，随聚合快照一起重建、整体替换。
type searchIndex struct {
	docs     []toolDoc
	byID     map[string]int
	bm25     *search.BM25
	bm25Docs []search.Doc
}

// collectToolDocs 读出"当前可被搜到"的工具：
// 属于已启用且可用（healthy/degraded）的 server，且通过该 server 的工具过滤名单。
//
// 检索索引与 embed 重建必须共用这一个函数：两边语料口径不一致，
// 增量重建会全部重嵌，检索也会返回 AI 实际调不到的工具。
func collectToolDocs(ctx context.Context, st *store.Store, fields search.CorpusFields) ([]toolDoc, error) {
	servers, err := st.ListServers(ctx)
	if err != nil {
		return nil, err
	}
	tools, err := st.ListTools(ctx)
	if err != nil {
		return nil, err
	}
	cats, err := st.ListCategories(ctx)
	if err != nil {
		return nil, err
	}
	catName := make(map[string]string, len(cats))
	for _, c := range cats {
		catName[c.ID] = c.Name
	}

	byServer := make(map[string][]store.Tool, len(servers))
	for _, t := range tools {
		byServer[t.ServerID] = append(byServer[t.ServerID], t)
	}

	out := make([]toolDoc, 0, len(tools))
	for _, sv := range servers {
		if !sv.Enabled || (sv.Status != "healthy" && sv.Status != "degraded") {
			continue
		}
		rows := byServer[sv.Spec.ID]
		if len(rows) == 0 {
			continue
		}
		// 过滤语义与聚合路径（applyToolFilter）完全一致：
		// 检索不该返回被 allow/block 名单藏起来的工具。
		infos := make([]downstream.ToolInfo, 0, len(rows))
		for _, t := range rows {
			infos = append(infos, downstream.ToolInfo{
				Name:        t.Name,
				Description: t.Description,
				InputSchema: json.RawMessage(t.InputSchema),
			})
		}
		kept := applyToolFilter(infos, sv.ToolFilterMode, sv.ToolFilterList)
		allow := nameSet(namesOf(kept))

		for _, t := range rows {
			if _, ok := allow[t.Name]; !ok {
				continue
			}
			out = append(out, toolDoc{
				ToolID:      t.ID,
				ServerID:    sv.Spec.ID,
				Exposed:     t.ExposedName,
				Name:        t.Name,
				ServerKey:   sv.Spec.Key,
				Description: t.Description,
				CategoryID:  sv.CategoryID,
				Category:    catName[sv.CategoryID],
				Tags:        sv.Tags,
				ServerDesc:  sv.Description,
				Schema:      json.RawMessage(t.InputSchema),
				Corpus:      search.CorpusText(t.Description, sv.Tags, sv.Description, fields),
			})
		}
	}
	return out, nil
}

func namesOf(list []downstream.ToolInfo) []string {
	out := make([]string, 0, len(list))
	for _, t := range list {
		out = append(out, t.Name)
	}
	return out
}

// docTokens 按 PRD §4.7.4 的字段权重展开词元：
// 工具名 ×3、tags ×2、工具描述 ×1、server 描述 ×1。
func docTokens(d toolDoc) []string {
	var toks []string
	toks = append(toks, repeatTokens(search.Tokenize(d.Name), 3)...)
	if len(d.Tags) > 0 {
		toks = append(toks, repeatTokens(search.Tokenize(strings.Join(d.Tags, " ")), 2)...)
	}
	toks = append(toks, search.Tokenize(d.Description)...)
	toks = append(toks, search.Tokenize(d.ServerDesc)...)
	return toks
}

// docRaw 是小写原文，keyword 模式的子串命中用。
// 带上对外全名：AI 照着列表复制 "weather__forecast" 来搜必须命中。
func docRaw(d toolDoc) string {
	return strings.ToLower(strings.Join([]string{
		d.Exposed, d.Name, d.Description, d.ServerDesc, strings.Join(d.Tags, " "),
	}, "\n"))
}

func repeatTokens(toks []string, n int) []string {
	if len(toks) == 0 {
		return nil
	}
	out := make([]string, 0, len(toks)*n)
	for i := 0; i < n; i++ {
		out = append(out, toks...)
	}
	return out
}

// rebuildSearchIndex 从库重建检索索引并整体替换。
func (s *Service) rebuildSearchIndex(ctx context.Context) {
	fields := search.DefaultCorpusFields()
	if em := s.embed; em != nil {
		fields = em.Config().CorpusFields
	}
	docs, err := collectToolDocs(ctx, s.store, fields)
	if err != nil {
		s.log.Warn("重建检索索引失败", zap.Error(err))
		return
	}

	ix := &searchIndex{
		docs: docs,
		byID: make(map[string]int, len(docs)),
	}
	bm25Docs := make([]search.Doc, 0, len(docs))
	for i := range docs {
		ix.byID[docs[i].ToolID] = i
		bm25Docs = append(bm25Docs, search.Doc{
			ID:     docs[i].ToolID,
			Tokens: docTokens(docs[i]),
			Raw:    docRaw(docs[i]),
		})
	}
	ix.bm25 = search.NewBM25(bm25Docs)
	ix.bm25Docs = bm25Docs

	s.searchMu.Lock()
	s.searchIX = ix
	s.searchMu.Unlock()
}

// idScore 是一条"工具 id + 分数"的中间结果。
//
// RRF 融合、rerank 重排全程用 tool id 流转，最后才映射回展示数据——
// 中途换成对外名会在"同名工具跨 server"的边界情况上串线。
type idScore struct {
	id    string
	score float64
}

// SearchTools 按三种模式检索工具（PRD §4.7.4）。
//
// 降级阶梯，每一级都保持可用：
//   - rerank 挂    → 保持融合顺序
//   - embed 挂     → 只用 bm25
//   - 无 pgvector  → keyword 与 bm25 融合
//
// 辅助能力挂掉不该让检索整体报错——搜不到比报错更接近可用。
func (s *Service) SearchTools(ctx context.Context, q SearchQuery) ([]ToolHit, error) {
	s.searchMu.Lock()
	ix := s.searchIX
	s.searchMu.Unlock()
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
		// 树式发现下默认 5：搜索的职责是给出候选方向而不是穷举，
		// 召回靠 facets 引导收窄补足，schema 靠 hub__tool 按需取。
		k = 5
	}
	if k > 50 {
		k = 50
	}

	// 过滤器在打分之后应用：BM25 的统计量（IDF）要在完整语料上算才准，
	// 先过滤再打分会让"过滤后恰好只剩一个含该词的工具"获得虚高的相关度。
	catID, _ := resolveCategory(ctx, s.store, q.Category)
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
		return hitsFromIDs(ix, s.bm25Branch(ix, query, k, match), q.WithSchema), nil

	default: // hybrid
		lists := [][]string{idsOf(s.bm25Branch(ix, query, 50, match), 50)}

		em := s.embed
		if em != nil {
			vecIDs, err := s.vectorBranch(ctx, ix, em, query, match)
			switch {
			case err == nil:
				lists = append(lists, vecIDs)
			case errors.Is(err, store.ErrVectorUnsupported):
				// 无 pgvector（SQLite 方言）：按 PRD 用 keyword+bm25 融合。
				lists = [][]string{
					idsOf(keywordBranch(ix, query, 50, match), 50),
					idsOf(s.bm25Branch(ix, query, 50, match), 50),
				}
			default:
				s.log.Warn("向量检索分支失败，退回 bm25", zap.Error(err))
			}
		} else {
			lists = [][]string{
				idsOf(keywordBranch(ix, query, 50, match), 50),
				idsOf(s.bm25Branch(ix, query, 50, match), 50),
			}
		}

		fused := search.RRF(search.DefaultRRFK, lists)
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
		if em != nil && em.Reranker() != nil && len(out) > 1 {
			texts := make([]string, 0, len(out))
			for _, sc := range out {
				texts = append(texts, ix.docs[ix.byID[sc.id]].Corpus)
			}
			ranked, err := em.Reranker().Rerank(ctx, query, texts, k)
			if err != nil {
				s.log.Warn("rerank 失败，保持融合顺序", zap.Error(err))
			} else {
				reordered := make([]idScore, 0, len(ranked))
				for _, r := range ranked {
					if r.Index >= 0 && r.Index < len(out) {
						reordered = append(reordered, idScore{id: out[r.Index].id, score: r.Score})
					}
				}
				out = reordered
			}
		}

		return hitsFromIDs(ix, out, q.WithSchema), nil
	}
}

// bm25Branch 跑 BM25 一路并应用过滤器，按分数降序返回 tool id。
func (s *Service) bm25Branch(ix *searchIndex, query string, k int, match func(toolDoc) bool) []idScore {
	want := k*5 + 10
	if len(ix.bm25Docs) < want {
		want = len(ix.bm25Docs)
	}
	out := make([]idScore, 0, k)
	for _, h := range ix.bm25.Search(search.Tokenize(query), want) {
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
	for _, id := range search.KeywordHits(query, ix.bm25Docs) {
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
func (s *Service) vectorBranch(ctx context.Context, ix *searchIndex, em *EmbedManager, query string, match func(toolDoc) bool) ([]string, error) {
	vec, err := em.EmbedQuery(ctx, query)
	if err != nil {
		return nil, err
	}
	cfg := em.Config()
	ids, err := s.store.SearchEmbeddings(ctx, cfg.Model, cfg.Dimensions, vec, 50)
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
		// 明确要 schema 时才把这份 KB 级的开销塞进返回里。
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

// resolveCategory 把 id 或名称解析成分类 id；都不是时原样返回（过滤条件落空，返回空结果）。
func resolveCategory(ctx context.Context, st *store.Store, nameOrID string) (string, error) {
	if nameOrID == "" {
		return "", nil
	}
	cats, err := st.ListCategories(ctx)
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
