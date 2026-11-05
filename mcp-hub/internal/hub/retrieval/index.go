package retrieval

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/hydra/mcp-hub/internal/hub/registry"
	"github.com/hydra/mcp-hub/internal/search/corpus"
	"github.com/hydra/mcp-hub/internal/search/lexical"
	"github.com/hydra/mcp-hub/internal/store"
	"github.com/hydra/mcp-hub/internal/store/servers"
	"go.uber.org/zap"
)

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
	Hints       string // 从 inputSchema 摘出的参数线索，语料与词法检索共用
	Corpus      string // 按当前语料字段组装的文本，embed 与 rerank 共用
}

// searchIndex 是检索的内存快照，随聚合快照一起重建、整体替换。
type searchIndex struct {
	docs []toolDoc
	byID map[string]int
	bm25 *lexical.BM25
}

// collectToolDocs 读出"当前可被搜到"的工具：属于已启用 server 且通过工具过滤名单。
//
// 检索索引与 embed 重建必须共用它：口径不一致会让增量重建退化为全量重嵌，
// 检索也会返回 AI 实际调不到的工具。badSchema 是 schema 解析失败的工具数，需上报但不让重建失败。
func collectToolDocs(ctx context.Context, st *store.Store, fields corpus.Fields) (docs []toolDoc, badSchema int, err error) {
	srvs, err := st.Servers.List(ctx)
	if err != nil {
		return nil, 0, err
	}
	tools, err := st.Servers.ListTools(ctx)
	if err != nil {
		return nil, 0, err
	}
	cats, err := st.Categories.List(ctx)
	if err != nil {
		return nil, 0, err
	}
	catName := make(map[string]string, len(cats))
	for _, c := range cats {
		catName[c.ID] = c.Name
	}

	byServer := make(map[string][]servers.Tool, len(srvs))
	for _, t := range tools {
		byServer[t.ServerID] = append(byServer[t.ServerID], t)
	}

	out := make([]toolDoc, 0, len(tools))
	for _, sv := range srvs {
		// 与聚合快照同口径：只要 server 启用且工具通过人工过滤，就在检索面里。
		// 不按 status 过滤——探活失败不清工具是全局约定，排除 offline 会让工具
		// "搜不到但仍在聚合列表里"，一次探活抖动还会让 server 从发现面消失。
		if !sv.Enabled {
			continue
		}
		rows := byServer[sv.Spec.ID]
		if len(rows) == 0 {
			continue
		}
		allow := make(map[string]struct{}, len(rows))
		for _, t := range rows {
			// 与聚合路径同源：人工禁用的工具既不暴露也不可被检索，
			// 否则会"搜得到但调不了"。
			if t.Enabled {
				allow[t.Name] = struct{}{}
			}
		}

		// 对外名与聚合路径同源，都按 <serverKey>__<toolName> 现算，
		// 不吃工具缓存里的 exposed_name——那是探活那一刻的快照。
		for _, t := range rows {
			if _, ok := allow[t.Name]; !ok {
				continue
			}
			schema := json.RawMessage(t.InputSchema)
			hints, hintErr := corpus.SchemaHints(schema)
			if hintErr != nil {
				badSchema++
			}
			out = append(out, toolDoc{
				ToolID:      t.ID,
				ServerID:    sv.Spec.ID,
				Exposed:     registry.ExposedToolName(sv.Spec.Key, t.Name),
				Name:        t.Name,
				ServerKey:   sv.Spec.Key,
				Description: t.Description,
				CategoryID:  sv.CategoryID,
				Category:    catName[sv.CategoryID],
				Tags:        sv.Tags,
				ServerDesc:  sv.Description,
				Schema:      schema,
				Hints:       hints,
				// 语料只吃"工具自身"的字段（工具名/描述/参数线索/标签/server 描述）。
				// 分类刻意不进这里：它是可变的组织元数据，进去意味改一次分类要重嵌整批向量；
				// 分类仍参与 docTokens，词法检索照样命中。
				Corpus: corpus.Text(t.Name, t.Description, sv.Tags, sv.Description, hints, fields),
			})
		}
	}
	return out, badSchema, nil
}

// docTokens 按字段权重展开词元：
// 工具名 ×3、tags ×2、分类名 ×1、工具描述 ×1、参数线索 ×1、server 描述 ×1。
// 词法索引不受语料字段开关影响；分类只出现在这里，是"改分类不重嵌向量"的补偿。
func docTokens(d toolDoc) []string {
	var toks []string
	toks = append(toks, repeatTokens(lexical.Tokenize(d.Name), 3)...)
	if len(d.Tags) > 0 {
		toks = append(toks, repeatTokens(lexical.Tokenize(strings.Join(d.Tags, " ")), 2)...)
	}
	toks = append(toks, lexical.Tokenize(d.Category)...)
	toks = append(toks, lexical.Tokenize(d.Description)...)
	toks = append(toks, lexical.Tokenize(d.Hints)...)
	toks = append(toks, lexical.Tokenize(d.ServerDesc)...)
	return toks
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

// RebuildIndex 从库重建检索索引并整体替换。
func (e *Engine) RebuildIndex(ctx context.Context) {
	fields := corpus.DefaultFields()
	if em := e.em; em != nil {
		fields = em.Config().CorpusFields
	}
	docs, badSchema, err := collectToolDocs(ctx, e.st, fields)
	if err != nil {
		e.log.Warn("重建检索索引失败", zap.Error(err))
		return
	}
	if badSchema > 0 {
		e.log.Warn("部分工具的 inputSchema 无法解析，其语料缺少参数线索",
			zap.Int("bad", badSchema), zap.Int("total", len(docs)))
	}

	ix := &searchIndex{
		docs: docs,
		byID: make(map[string]int, len(docs)),
	}
	bm25Docs := make([]lexical.Doc, 0, len(docs))
	for i := range docs {
		ix.byID[docs[i].ToolID] = i
		bm25Docs = append(bm25Docs, lexical.Doc{
			ID:     docs[i].ToolID,
			Tokens: docTokens(docs[i]),
		})
	}
	ix.bm25 = lexical.NewBM25(bm25Docs)

	e.mu.Lock()
	e.ix = ix
	e.mu.Unlock()
}
