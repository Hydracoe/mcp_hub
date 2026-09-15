package hub_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/hydra/mcp-hub/internal/downstream"
	"github.com/hydra/mcp-hub/internal/httpd"
	"github.com/hydra/mcp-hub/internal/hub"
	"github.com/hydra/mcp-hub/internal/store"
	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
)

// ---- 测试基建 --------------------------------------------------------------

// newMetaEnv 起一个 meta 列表模式的完整网关（PRD §4.7.6）。
//
// 与 newEnv 的唯一区别：ListMode=meta 且挂上空 embed 管理器
// （注册 meta-tool，不启用向量检索）。
func newMetaEnv(t *testing.T, servers []downstream.Spec) *env {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "meta.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	ctx := context.Background()
	for _, spec := range servers {
		if err := st.UpsertServer(ctx, store.Config{Spec: spec, Enabled: true}); err != nil {
			t.Fatalf("写入 server %s 失败: %v", spec.Key, err)
		}
	}

	svc := hub.NewServiceWithOptions(st, nil, hub.Options{ListMode: hub.ListModeMeta})
	t.Cleanup(func() { svc.Close() })
	svc.AttachEmbedding(nil)

	if len(servers) > 0 {
		svc.RefreshAll(ctx)
	}

	srv := httpd.New(svc, httpd.Options{EnableSSE: false})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	trans, err := transport.NewStreamableHTTP(ts.URL + "/mcp")
	if err != nil {
		t.Fatalf("构造传输失败: %v", err)
	}
	c := client.NewClient(trans)
	t.Cleanup(func() { c.Close() })

	if err := c.Start(ctx); err != nil {
		t.Fatalf("启动 MCP 客户端失败: %v", err)
	}
	if _, err := c.Initialize(ctx, mcp.InitializeRequest{
		Params: mcp.InitializeParams{
			ClientInfo:   mcp.Implementation{Name: "test", Version: "0"},
			Capabilities: mcp.ClientCapabilities{},
		},
	}); err != nil {
		t.Fatalf("初始化失败: %v", err)
	}

	return &env{svc: svc, store: st, ts: ts, client: c}
}

// openSvc 只起服务层（不挂 HTTP 与客户端），给纯服务级断言用。
func openSvc(t *testing.T) (*store.Store, *hub.Service) {
	t.Helper()

	st, err := store.Open(filepath.Join(t.TempDir(), "svc.db"))
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	svc := hub.NewService(st, nil)
	t.Cleanup(func() { svc.Close() })
	return st, svc
}

// fakeModel 是测试用的 OpenAI 兼容 embed 服务 + Jina 风格 rerank 服务。
//
// vecOf 决定每条文本的向量；rerank 端点按输入倒序返回，
// 用于验证"精排确实改变了最终顺序"。
type fakeModel struct {
	mu      sync.Mutex
	embeds  int
	reranks []fakeRerankCall
	vecOf   func(text string) []float64
}

type fakeRerankCall struct {
	Query     string
	Documents []string
	TopN      int
}

func (f *fakeModel) embedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.embeds
}

func (f *fakeModel) rerankCalls() []fakeRerankCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]fakeRerankCall, len(f.reranks))
	copy(out, f.reranks)
	return out
}

func (f *fakeModel) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/embeddings", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.embeds++
		f.mu.Unlock()

		data := make([]map[string]any, len(body.Input))
		for i, text := range body.Input {
			data[i] = map[string]any{"index": i, "embedding": f.vecOf(text)}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"model": body.Model, "data": data})
	})
	mux.HandleFunc("/rerank", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Query     string   `json:"query"`
			Documents []string `json:"documents"`
			TopN      int      `json:"top_n"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.reranks = append(f.reranks, fakeRerankCall{
			Query: body.Query, Documents: body.Documents, TopN: body.TopN,
		})
		f.mu.Unlock()

		results := make([]map[string]any, len(body.Documents))
		for i := range body.Documents {
			results[i] = map[string]any{
				"index":           len(body.Documents) - 1 - i,
				"relevance_score": 1.0 - 0.1*float64(i),
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"results": results})
	})
	return mux
}

// fakeModelURL 起一个假的 embed/rerank 服务，返回其实例与基础 URL。
func fakeModelURL(t *testing.T, vecOf func(text string) []float64) (*fakeModel, string) {
	t.Helper()
	fm := &fakeModel{vecOf: vecOf}
	ts := httptest.NewServer(fm.handler())
	t.Cleanup(ts.Close)
	return fm, ts.URL
}

// searchResults 是 hub__search_tools 返回 JSON 的形状。
type searchResults struct {
	Count   int `json:"count"`
	Results []struct {
		ExposedName string `json:"exposedName"`
		Name        string `json:"name"`
		ServerKey   string `json:"serverKey"`
	} `json:"results"`
}

func metaJSON(t *testing.T, res *mcp.CallToolResult, v any) {
	t.Helper()
	if res.IsError {
		t.Fatalf("meta-tool 返回错误: %s", textOf(t, res))
	}
	if err := json.Unmarshal([]byte(textOf(t, res)), v); err != nil {
		t.Fatalf("解析 meta-tool 结果失败: %v", err)
	}
}

func hitContains(hits []hub.ToolHit, exposed string) bool {
	for _, h := range hits {
		if h.Exposed == exposed {
			return true
		}
	}
	return false
}

// seedSearchServer 建一个带两个工具（中文描述）的 server，返回其 id。
func seedSearchServer(t *testing.T, svc *hub.Service, key string) string {
	t.Helper()
	down := startDownstream(t,
		toolSpec{name: "forecast", desc: "查询城市天气"},
		toolSpec{name: "geocode", desc: "把地址转成经纬度"},
	)
	res, err := svc.ApplyServer(context.Background(), store.Config{
		Spec:    remoteSpec(key, down),
		Enabled: true,
		Tags:    []string{"天气"},
	})
	if err != nil {
		t.Fatalf("ApplyServer 失败: %v", err)
	}
	svc.RefreshAll(context.Background())
	return res.ID
}

// ---- meta 列表模式（PRD §4.7.6）-------------------------------------------

func TestMetaModeListOnlyMetaTools(t *testing.T) {
	down := startDownstream(t,
		toolSpec{name: "echo", desc: "Repeat the given text"},
		toolSpec{name: "now", desc: "Return the current time"},
	)
	e := newMetaEnv(t, []downstream.Spec{remoteSpec("echo", down)})

	tools := e.listTools(t)
	if len(tools) != 5 {
		t.Fatalf("meta 模式应列出 5 个 meta-tool，实际 %d", len(tools))
	}
	for _, tl := range tools {
		if !strings.HasPrefix(tl.Name, hub.MetaPrefix) {
			t.Fatalf("meta 模式列出了非 meta 工具 %q", tl.Name)
		}
	}

	// 下游工具不在列表里，但按对外全名照样能直接调用。
	res := e.callTool(t, "echo__echo", `{"text":"hi"}`)
	if res.IsError {
		t.Fatalf("meta 模式下下游工具应可直接调用: %s", textOf(t, res))
	}
}

func TestMetaModeAllFiveToolsCallable(t *testing.T) {
	down := startDownstream(t,
		toolSpec{name: "forecast", desc: "查询城市天气"},
	)
	e := newMetaEnv(t, []downstream.Spec{remoteSpec("wx", down)})

	// search_tools：按意图搜到下游工具
	var sr searchResults
	metaJSON(t, e.callTool(t, "hub__search_tools", `{"query":"天气"}`), &sr)
	if sr.Count == 0 || sr.Results[0].ExposedName != "wx__forecast" {
		t.Fatalf("search_tools 未命中预期工具: %+v", sr)
	}

	// list_categories：空库返回空数组而非报错
	var cats []map[string]any
	metaJSON(t, e.callTool(t, "hub__list_categories", `{}`), &cats)
	if len(cats) != 0 {
		t.Fatalf("空库应返回空分类列表: %+v", cats)
	}

	// list_servers：只列已启用 server，带可用性与鉴权标识
	var svrs []map[string]any
	metaJSON(t, e.callTool(t, "hub__list_servers", `{}`), &svrs)
	if len(svrs) != 1 || svrs[0]["key"] != "wx" || svrs[0]["available"] != true {
		t.Fatalf("list_servers 结果不符: %+v", svrs)
	}
	if svrs[0]["authMode"] != "none" {
		t.Fatalf("无 headers 的 server 应标识为免鉴权: %+v", svrs[0])
	}

	// list_tools：按 server 过滤
	var tl []map[string]any
	metaJSON(t, e.callTool(t, "hub__list_tools", `{"server":"wx"}`), &tl)
	if len(tl) != 1 || tl[0]["exposedName"] != "wx__forecast" {
		t.Fatalf("list_tools 结果不符: %+v", tl)
	}

	// tool：按对外名取完整定义
	var def map[string]any
	metaJSON(t, e.callTool(t, "hub__tool", `{"name":"wx__forecast"}`), &def)
	if def["name"] != "forecast" || def["serverKey"] != "wx" {
		t.Fatalf("tool 定义不符: %+v", def)
	}

	// tool：未知名 → isError + 错误自愈提示
	miss := e.callTool(t, "hub__tool", `{"name":"nope__x"}`)
	if !miss.IsError {
		t.Fatal("未知工具应返回 isError")
	}
	if !strings.Contains(textOf(t, miss), "hub__search_tools") {
		t.Fatalf("未知工具错误应提示下一步: %s", textOf(t, miss))
	}

	// search_tools：空 query → isError（错误自愈，不是协议错误）
	bad := e.callTool(t, "hub__search_tools", `{}`)
	if !bad.IsError {
		t.Fatal("空 query 应返回 isError")
	}
}

// ---- 树式发现：薄结果 + facets（PRD §4.7.6）--------------------------------

func TestSearchThinResultsWithFacets(t *testing.T) {
	down := startDownstream(t,
		toolSpec{name: "forecast", desc: "查询城市天气"},
		toolSpec{name: "geocode", desc: "把地址转成经纬度"},
	)
	e := newMetaEnv(t, []downstream.Spec{remoteSpec("wx", down)})

	// 默认薄结果：不带 inputSchema，但带 facets 引导 AI 收窄。
	var sr struct {
		Count  int `json:"count"`
		Facets struct {
			Servers []struct {
				Name string `json:"name"`
				Hits int    `json:"hits"`
			} `json:"servers"`
		} `json:"facets"`
		Results []struct {
			ExposedName string          `json:"exposedName"`
			InputSchema json.RawMessage `json:"inputSchema"`
		} `json:"results"`
	}
	metaJSON(t, e.callTool(t, "hub__search_tools", `{"query":"天气"}`), &sr)
	if sr.Count == 0 {
		t.Fatal("搜索应命中工具")
	}
	for _, r := range sr.Results {
		if len(r.InputSchema) != 0 {
			t.Fatalf("薄结果不应内联 inputSchema: %+v", r)
		}
	}
	if len(sr.Facets.Servers) == 0 || sr.Facets.Servers[0].Name != "wx" ||
		sr.Facets.Servers[0].Hits != sr.Count {
		t.Fatalf("facets 应汇总 server 命中分布: %+v", sr.Facets)
	}

	// with_schema=true：一次直达的退路保留，结果带完整参数定义。
	var sr2 struct {
		Results []struct {
			ExposedName string          `json:"exposedName"`
			InputSchema json.RawMessage `json:"inputSchema"`
		} `json:"results"`
	}
	metaJSON(t, e.callTool(t, "hub__search_tools", `{"query":"天气","with_schema":true}`), &sr2)
	if len(sr2.Results) == 0 || len(sr2.Results[0].InputSchema) == 0 {
		t.Fatalf("with_schema=true 应内联 inputSchema: %+v", sr2)
	}
}

// ---- 检索三模式与降级（PRD §4.7.4）----------------------------------------

func TestSearchToolsModes(t *testing.T) {
	_, svc := openSvc(t)
	ctx := context.Background()
	seedSearchServer(t, svc, "wx")

	// keyword：词面命中
	hits, err := svc.SearchTools(ctx, hub.SearchQuery{Query: "天气", Mode: "keyword"})
	if err != nil {
		t.Fatalf("keyword 检索失败: %v", err)
	}
	if !hitContains(hits, "wx__forecast") {
		t.Fatalf("keyword 应命中 forecast: %+v", hits)
	}

	// bm25：分词命中（bigram 天气）
	hits, err = svc.SearchTools(ctx, hub.SearchQuery{Query: "天气", Mode: "bm25"})
	if err != nil {
		t.Fatalf("bm25 检索失败: %v", err)
	}
	if !hitContains(hits, "wx__forecast") {
		t.Fatalf("bm25 应命中 forecast: %+v", hits)
	}

	// hybrid（未配 embed）：降级 keyword+bm25，仍然可用
	hits, err = svc.SearchTools(ctx, hub.SearchQuery{Query: "天气"})
	if err != nil {
		t.Fatalf("hybrid 降级检索失败: %v", err)
	}
	if !hitContains(hits, "wx__forecast") {
		t.Fatalf("hybrid 降级应命中 forecast: %+v", hits)
	}

	// 过滤器：server 大小写不敏感；落空的过滤返回空
	hits, err = svc.SearchTools(ctx, hub.SearchQuery{Query: "天气", Mode: "keyword", Server: "WX"})
	if err != nil || !hitContains(hits, "wx__forecast") {
		t.Fatalf("server 过滤（大写）应命中: %+v err=%v", hits, err)
	}
	hits, err = svc.SearchTools(ctx, hub.SearchQuery{Query: "天气", Mode: "keyword", Server: "other"})
	if err != nil || len(hits) != 0 {
		t.Fatalf("server 过滤落空应返回空: %+v err=%v", hits, err)
	}
	hits, err = svc.SearchTools(ctx, hub.SearchQuery{Query: "天气", Mode: "keyword", Tag: "天气"})
	if err != nil || len(hits) == 0 {
		t.Fatalf("tag 过滤应命中: %+v err=%v", hits, err)
	}

	// k 钳制
	hits, err = svc.SearchTools(ctx, hub.SearchQuery{Query: "天气", Mode: "keyword", K: 1})
	if err != nil || len(hits) > 1 {
		t.Fatalf("k=1 应至多返回 1 条: %+v err=%v", hits, err)
	}

	// 非法输入
	if _, err = svc.SearchTools(ctx, hub.SearchQuery{Query: "天气", Mode: "bogus"}); err == nil {
		t.Fatal("非法模式应报错")
	}
	if _, err = svc.SearchTools(ctx, hub.SearchQuery{Query: "  ", Mode: "keyword"}); err == nil {
		t.Fatal("空 query 应报错")
	}
}

// ---- 嵌入重建与 rerank（PRD §4.7.5）---------------------------------------

func TestEmbedRebuildIncremental(t *testing.T) {
	st, svc := openSvc(t)
	ctx := context.Background()
	id := seedSearchServer(t, svc, "wx")

	constVec := func(string) []float64 { return []float64{1, 0, 0} }
	fm, modelURL := fakeModelURL(t, constVec)
	em := hub.NewEmbedManager(st, nil, hub.EmbedOptions{
		BaseURL:    modelURL,
		APIKey:     "k",
		Model:      "test-model",
		Dimensions: 3,
	})
	svc.AttachEmbedding(em)

	// 首次重建：两个工具都有语料
	rs := em.Rebuild(ctx)
	if rs.State != hub.RebuildDone || rs.Total != 2 || rs.Done != 2 {
		t.Fatalf("首次重建不符: %+v", rs)
	}
	if got, _ := st.ListEmbeddings(ctx); len(got) != 2 {
		t.Fatalf("应落 2 条向量，实际 %d", len(got))
	}

	// 语料未变：不重嵌
	before := fm.embedCount()
	rs = em.Rebuild(ctx)
	if rs.Total != 0 || fm.embedCount() != before {
		t.Fatalf("语料未变不应重嵌: total=%d embeds=%d→%d", rs.Total, before, fm.embedCount())
	}

	// server 描述变更：语料 hash 变，全部重嵌
	if _, err := svc.ApplyServer(ctx, store.Config{
		Spec:        remoteSpec("wx", deadURL(t)),
		Enabled:     true,
		Description: "新的描述",
	}); err != nil {
		t.Fatalf("更新描述失败: %v", err)
	}
	rs = em.Rebuild(ctx)
	if rs.Total != 2 || rs.Done != 2 {
		t.Fatalf("语料变更应重嵌 2 条: %+v", rs)
	}

	// server 停用：向量清掉
	if err := svc.SetEnabled(ctx, id, false); err != nil {
		t.Fatalf("停用失败: %v", err)
	}
	before = fm.embedCount()
	rs = em.Rebuild(ctx)
	if rs.State != hub.RebuildDone || rs.Total != 0 {
		t.Fatalf("停用后应无待嵌: %+v", rs)
	}
	if got, _ := st.ListEmbeddings(ctx); len(got) != 0 {
		t.Fatalf("停用后向量应清空，实际 %d", len(got))
	}
	if fm.embedCount() != before {
		t.Fatal("停用后不应有新的 embed 请求")
	}
}

func TestSearchHybridRerankReorders(t *testing.T) {
	_, svc := openSvc(t)
	ctx := context.Background()
	seedSearchServer(t, svc, "wx")

	constVec := func(string) []float64 { return []float64{1, 0, 0} }
	fm, modelURL := fakeModelURL(t, constVec)
	em := hub.NewEmbedManager(svc.Store(), nil, hub.EmbedOptions{
		BaseURL:     modelURL,
		APIKey:      "k",
		Model:       "test-model",
		Dimensions:  3,
		RerankModel: "test-rerank",
	})
	svc.AttachEmbedding(em)
	if rs := em.Rebuild(ctx); rs.State != hub.RebuildDone {
		t.Fatalf("重建失败: %+v", rs)
	}

	// SQLite 无 pgvector：向量分支走 ErrVectorUnsupported 降级 keyword+bm25，
	// 但 rerank 不受影响——精排请求必须发出，且最终顺序按精排结果倒序。
	hits, err := svc.SearchTools(ctx, hub.SearchQuery{Query: "天气 经纬度", K: 2})
	if err != nil {
		t.Fatalf("hybrid 检索失败: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("应返回 2 条: %+v", hits)
	}

	calls := fm.rerankCalls()
	if len(calls) != 1 {
		t.Fatalf("应恰好一次 rerank 请求，实际 %d", len(calls))
	}
	rc := calls[0]
	if rc.Query != "天气 经纬度" || rc.TopN != 2 || len(rc.Documents) != 2 {
		t.Fatalf("rerank 请求体不符: %+v", rc)
	}

	docOf := func(doc string) string {
		if strings.Contains(doc, "天气") {
			return "forecast"
		}
		return "geocode"
	}
	// 假 rerank 按输入倒序返回：最终第 1 条 = 送排的最后一条。
	wantFirst := docOf(rc.Documents[1])
	if hits[0].Name != wantFirst {
		t.Fatalf("rerank 重排未生效: 首条 %s，期望 %s", hits[0].Name, wantFirst)
	}
}

func TestEmbedConfigUpdateAndPersistence(t *testing.T) {
	st, _ := openSvc(t)
	ctx := context.Background()

	base := hub.EmbedConfig{
		Model: "m1", Dimensions: 8, BaseURL: "http://x", RerankModel: "r1",
	}
	em := hub.NewEmbedManager(st, nil, hub.EmbedOptions{
		BaseURL: base.BaseURL, APIKey: "k", Model: base.Model,
		Dimensions: base.Dimensions, RerankModel: base.RerankModel,
	})

	// 相同配置：不触发重建
	changed, err := em.UpdateConfig(ctx, base)
	if err != nil || changed {
		t.Fatalf("相同配置不应触发重建: changed=%v err=%v", changed, err)
	}

	// 换模型：触发重建，且落库
	changed, err = em.UpdateConfig(ctx, hub.EmbedConfig{
		Model: "m2", Dimensions: 8, BaseURL: "http://x", RerankModel: "r1",
	})
	if err != nil || !changed {
		t.Fatalf("换模型应触发重建: changed=%v err=%v", changed, err)
	}

	// 新进程（新管理器）从库里读回 m2
	em2 := hub.NewEmbedManager(st, nil, hub.EmbedOptions{APIKey: "k", Model: "WRONG", Dimensions: 1})
	if got := em2.Config(); got.Model != "m2" || got.Dimensions != 8 {
		t.Fatalf("配置未持久化: %+v", got)
	}

	// 校验
	if _, err := em.UpdateConfig(ctx, hub.EmbedConfig{Model: "", Dimensions: 8, BaseURL: "http://x"}); err == nil {
		t.Fatal("空 model 应拒绝")
	}
	if _, err := em.UpdateConfig(ctx, hub.EmbedConfig{Model: "m", Dimensions: 0, BaseURL: "http://x"}); err == nil {
		t.Fatal("零维度应拒绝")
	}
}

// ---- 自动归类（PRD §4.7.3）------------------------------------------------

func TestAutoCategorizeThreshold(t *testing.T) {
	st, svc := openSvc(t)
	ctx := context.Background()

	constVec := func(string) []float64 { return []float64{1, 0} }
	_, modelURL := fakeModelURL(t, constVec)
	em := hub.NewEmbedManager(st, nil, hub.EmbedOptions{
		BaseURL: modelURL, APIKey: "k", Model: "m", Dimensions: 2,
	})
	svc.AttachEmbedding(em)

	if _, err := svc.CreateCategory(ctx, "天气", "天气查询"); err != nil {
		t.Fatalf("建分类失败: %v", err)
	}
	if _, err := svc.CreateCategory(ctx, "地图", "地址解析"); err != nil {
		t.Fatalf("建分类失败: %v", err)
	}

	// 常量向量 → 余弦全 1 → top-1 过线，自动应用
	applied, sugg, err := em.AutoCategorize(ctx, "北京天气")
	if err != nil {
		t.Fatalf("AutoCategorize 失败: %v", err)
	}
	if applied == nil {
		t.Fatalf("相似度 1.0 应自动应用分类: sugg=%+v", sugg)
	}
	if len(sugg) != 2 || sugg[0].Score < 1.0 {
		t.Fatalf("建议应含 2 项且首项满分: %+v", sugg)
	}
	// 两个分类同分（都 1.0），top-1 按 id 升序取，必须是二者之一。
	cats, _ := svc.Categories(ctx)
	minID := cats[0].ID
	if len(cats) > 1 && cats[1].ID < minID {
		minID = cats[1].ID
	}
	if applied.ID != minID {
		t.Fatalf("同分应按 id 升序取 top-1: got %s want %s", applied.ID, minID)
	}

	// 空文本：直接空结果
	applied, sugg, err = em.AutoCategorize(ctx, "   ")
	if err != nil || applied != nil || sugg != nil {
		t.Fatalf("空文本应返回空: applied=%v sugg=%v err=%v", applied, sugg, err)
	}

	// 无分类：空结果
	st2, svc2 := openSvc(t)
	_, modelURL2 := fakeModelURL(t, constVec)
	em2 := hub.NewEmbedManager(st2, nil, hub.EmbedOptions{
		BaseURL: modelURL2, APIKey: "k", Model: "m", Dimensions: 2,
	})
	svc2.AttachEmbedding(em2)
	applied, sugg, err = em2.AutoCategorize(ctx, "任意文本")
	if err != nil || applied != nil || sugg != nil {
		t.Fatalf("无分类应返回空: applied=%v sugg=%v err=%v", applied, sugg, err)
	}
}

func TestApplyServerAutoCategorize(t *testing.T) {
	st, svc := openSvc(t)
	ctx := context.Background()

	constVec := func(string) []float64 { return []float64{1, 0} }
	_, modelURL := fakeModelURL(t, constVec)
	em := hub.NewEmbedManager(st, nil, hub.EmbedOptions{
		BaseURL: modelURL, APIKey: "k", Model: "m", Dimensions: 2,
	})
	svc.AttachEmbedding(em)

	cat, err := svc.CreateCategory(ctx, "天气", "天气查询")
	if err != nil {
		t.Fatalf("建分类失败: %v", err)
	}

	down := startDownstream(t, toolSpec{name: "forecast", desc: "查询城市天气"})
	res, err := svc.ApplyServer(ctx, store.Config{
		Spec:    remoteSpec("wx", down),
		Enabled: true,
		// 不指定 CategoryID：应触发 embedding 自动归类
	})
	if err != nil || !res.Created {
		t.Fatalf("建 server 失败: err=%v created=%v", err, res.Created)
	}
	if len(res.SuggestedCategories) == 0 {
		t.Fatalf("应返回分类建议: %+v", res)
	}
	sv, err := st.GetServerByKey(ctx, "wx")
	if err != nil {
		t.Fatalf("读 server 失败: %v", err)
	}
	if sv.CategoryID != cat.ID {
		t.Fatalf("server 应被自动归入唯一分类: got %q want %q", sv.CategoryID, cat.ID)
	}
}

// ---- 分类生命周期（PRD §4.7.3）---------------------------------------------

func TestCategoryLifecycle(t *testing.T) {
	st, svc := openSvc(t)
	ctx := context.Background()

	c1, err := svc.CreateCategory(ctx, "天气", "")
	if err != nil {
		t.Fatalf("建分类失败: %v", err)
	}
	if _, err := svc.CreateCategory(ctx, "天气", ""); !errors.Is(err, store.ErrCategoryTaken) {
		t.Fatalf("重名应报 ErrCategoryTaken，实际 %v", err)
	}

	c2, err := svc.UpdateCategory(ctx, c1.ID, "天气预报", "")
	if err != nil || c2.Name != "天气预报" {
		t.Fatalf("改名失败: %+v err=%v", c2, err)
	}
	if _, err := svc.UpdateCategory(ctx, "no-such-id", "x", ""); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("改不存在的分类应报 ErrNotFound，实际 %v", err)
	}

	// server 显式指定分类（跳过自动归类）
	down := startDownstream(t, toolSpec{name: "forecast", desc: "查询城市天气"})
	res, err := svc.ApplyServer(ctx, store.Config{
		Spec:       remoteSpec("wx", down),
		Enabled:    true,
		CategoryID: c2.ID,
	})
	if err != nil || !res.Created {
		t.Fatalf("建 server 失败: err=%v", err)
	}
	sv, err := st.GetServerByKey(ctx, "wx")
	if err != nil || sv.CategoryID != c2.ID {
		t.Fatalf("server 应带分类: %+v err=%v", sv, err)
	}

	// 检索索引里能看到分类名，按名称过滤可用
	svc.RefreshAll(ctx)
	hits, err := svc.SearchTools(ctx, hub.SearchQuery{
		Query: "天气", Mode: "keyword", Category: "天气预报",
	})
	if err != nil || !hitContains(hits, "wx__forecast") {
		t.Fatalf("按分类名检索应命中: %+v err=%v", hits, err)
	}

	// 删分类：引用被清空
	if err := svc.DeleteCategory(ctx, c2.ID); err != nil {
		t.Fatalf("删分类失败: %v", err)
	}
	sv, _ = st.GetServerByKey(ctx, "wx")
	if sv.CategoryID != "" {
		t.Fatalf("删分类后 server 引用应清空，实际 %q", sv.CategoryID)
	}
}
