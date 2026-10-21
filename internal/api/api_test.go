package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hydra/mcp-hub/internal/cache/cachetest"
	"github.com/hydra/mcp-hub/internal/httpd"
	"github.com/hydra/mcp-hub/internal/hub"
	"github.com/hydra/mcp-hub/internal/hub/registry"
	"github.com/hydra/mcp-hub/internal/store"
	"github.com/hydra/mcp-hub/internal/store/storetest"
	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"go.uber.org/zap"
)

// --- 测试环境 ---

// env 是一套跑起来的网关：真实 PostgreSQL + 真实 MCP 下游 + 真实 HTTP 层。
//
// 全部走真家伙，不打桩：管理接口的价值恰恰在于"它改的东西真的生效"，
// 而 mock 掉中间的任意一层，这个性质就没法验证了。
type env struct {
	t     *testing.T
	svc   *hub.Service
	store *store.Store
	ts    *httptest.Server
}

func newEnv(t *testing.T, opts httpd.Options) *env {
	t.Helper()

	st, err := store.Open(storetest.DSN(t))
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	svc := hub.NewServiceWithCache(st, nil, cachetest.New())
	t.Cleanup(func() { _ = svc.Close() })

	if opts.Logger == nil {
		// 默认只在出错时说话，免得测试输出被访问日志淹没。
		opts.Logger = zap.NewNop()
	}
	opts.Addr = ""

	ts := httptest.NewServer(httpd.New(svc, opts).Handler())
	t.Cleanup(ts.Close)

	return &env{t: t, svc: svc, store: st, ts: ts}
}

func (e *env) call(method, path string, body any) (*http.Response, []byte) {
	e.t.Helper()
	return e.callH(method, path, body, nil)
}

func (e *env) callH(method, path string, body any, hdr map[string]string) (*http.Response, []byte) {
	e.t.Helper()

	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			e.t.Fatalf("序列化请求体失败: %v", err)
		}
		reader = bytes.NewReader(raw)
	}

	req, err := http.NewRequest(method, e.ts.URL+path, reader)
	if err != nil {
		e.t.Fatalf("构造请求失败: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatalf("%s %s 失败: %v", method, path, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		e.t.Fatalf("读取响应失败: %v", err)
	}
	return resp, raw
}

// mustJSON 发一个请求，要求状态码符合预期，并把响应解进 out。
func (e *env) mustJSON(method, path string, body any, wantStatus int, out any) {
	e.t.Helper()

	resp, raw := e.call(method, path, body)
	if resp.StatusCode != wantStatus {
		e.t.Fatalf("%s %s = %d, 期望 %d\n响应: %s", method, path, resp.StatusCode, wantStatus, raw)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			e.t.Fatalf("解析 %s %s 的响应失败: %v\n响应: %s", method, path, err, raw)
		}
	}
}

func (e *env) get(path string, out any) {
	e.t.Helper()
	e.mustJSON(http.MethodGet, path, nil, http.StatusOK, out)
}

// enableAllTools 启用当前所有缓存工具并重建聚合快照。
//
// 生产语义是"新探活发现的工具默认禁用，由管理员逐个开启"；下面这些用例
// 验证的是开启之后的聚合/过滤/调用链路，统一显式启用以保持用例意图，
// 默认禁用与人工启停另有专门用例覆盖。
func (e *env) enableAllTools() {
	e.t.Helper()
	ctx := context.Background()
	tools, err := e.store.Servers.ListTools(ctx)
	if err != nil {
		e.t.Fatalf("读取工具缓存失败: %v", err)
	}
	for _, tl := range tools {
		if tl.Enabled {
			continue
		}
		if err := e.svc.SetToolEnabled(ctx, tl.ServerID, tl.Name, true); err != nil {
			e.t.Fatalf("启用工具 %s 失败: %v", tl.Name, err)
		}
	}
}

// mustCategory 建一个分类并返回 id（同环境内重复调用直接复用）。
//
// 新建 server 必须指定分类，所以每条创建用例都要带一个。
// 分类不进对外工具名（对外名是 <serverKey>__<toolName>），
// 所以这里的分类叫什么都不影响断言里的期望名。
func (e *env) mustCategory() string {
	e.t.Helper()
	var list struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	e.get("/api/categories", &list)
	if len(list.Items) > 0 {
		return list.Items[0].ID
	}
	var cat struct {
		ID string `json:"id"`
	}
	e.mustJSON(http.MethodPost, "/api/categories", map[string]any{
		"name": "天气", "slug": "weather",
	}, http.StatusCreated, &cat)
	return cat.ID
}

// mcpClient 以 AI 客户端的身份连上聚合端点。
func (e *env) mcpClient() *client.Client {
	e.t.Helper()

	trans, err := transport.NewStreamableHTTP(e.ts.URL + "/mcp")
	if err != nil {
		e.t.Fatalf("构造 MCP 传输失败: %v", err)
	}
	c := client.NewClient(trans)
	e.t.Cleanup(func() { _ = c.Close() })

	ctx := context.Background()
	if err := c.Start(ctx); err != nil {
		e.t.Fatalf("启动 MCP 客户端失败: %v", err)
	}
	if _, err := c.Initialize(ctx, mcp.InitializeRequest{
		Params: mcp.InitializeParams{
			ClientInfo: mcp.Implementation{Name: "api-test", Version: "0"},
		},
	}); err != nil {
		e.t.Fatalf("MCP 初始化失败: %v", err)
	}
	return c
}

// --- 下游与响应结构 ---

// startDownstream 起一个真实的下游 MCP server。
func startDownstream(t *testing.T, tools ...string) string {
	t.Helper()

	srv := mcpserver.NewMCPServer("fake-downstream", "1.0.0",
		mcpserver.WithToolCapabilities(false))
	for _, name := range tools {
		srv.AddTool(
			mcp.NewTool(name, mcp.WithDescription("description of "+name)),
			func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return mcp.NewToolResultText(req.Params.Name + " called"), nil
			},
		)
	}

	ts := httptest.NewServer(mcpserver.NewStreamableHTTPServer(srv))
	t.Cleanup(ts.Close)
	return ts.URL
}

// deadURL 返回一个确定连不上的地址。
func deadURL(t *testing.T) string {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := ts.URL
	ts.Close()
	return url
}

// serverResp 是 server 相关接口的响应。
type serverResp struct {
	ID               string `json:"id"`
	Key              string `json:"key"`
	Name             string `json:"name"`
	Transport        string `json:"transport"`
	Target           string `json:"target"`
	Enabled          bool   `json:"enabled"`
	AutoEnable       bool   `json:"autoEnable"`
	Status           string `json:"status"`
	ProtocolEra      string `json:"protocolEra"`
	ProtocolVersion  string `json:"protocolVersion"`
	ToolCount        int    `json:"toolCount"`
	ExposedToolCount int    `json:"exposedToolCount"`
	LastErrorKind    string `json:"lastErrorKind"`
	LastErrorHint    string `json:"lastErrorHint"`
	LastLatencyMS    int64  `json:"lastLatencyMs"`

	Tools []string `json:"tools"`

	Config struct {
		URL        string            `json:"url"`
		Headers    map[string]string `json:"headers"`
		HeaderKeys []string          `json:"headerKeys"`
		Env        []string          `json:"env"`
		TimeoutMS  int64             `json:"timeoutMs"`
	} `json:"config"`

	Probe *struct {
		OK              bool     `json:"ok"`
		Kind            string   `json:"kind"`
		Hint            string   `json:"hint"`
		Stage           string   `json:"stage"`
		Detail          string   `json:"detail"`
		ToolCount       int      `json:"toolCount"`
		ProtocolVersion string   `json:"protocolVersion"`
		ServerName      string   `json:"serverName"`
		Legacy          bool     `json:"legacy"`
		Exposed         []string `json:"exposedTools"`
		Latency         struct {
			Total int64 `json:"totalMs"`
		} `json:"latency"`
	} `json:"probe"`
}

// probeResp 是 /test 与 /refresh-tools 的响应（探活结果本身就是顶层结构）。
type probeResp struct {
	OK              bool     `json:"ok"`
	Kind            string   `json:"kind"`
	Hint            string   `json:"hint"`
	Stage           string   `json:"stage"`
	Detail          string   `json:"detail"`
	ToolCount       int      `json:"toolCount"`
	ProtocolVersion string   `json:"protocolVersion"`
	ServerName      string   `json:"serverName"`
	Legacy          bool     `json:"legacy"`
	Exposed         []string `json:"exposedTools"`
	Latency         struct {
		Total int64 `json:"totalMs"`
	} `json:"latency"`
}

type serverListResp struct {
	Total int          `json:"total"`
	Items []serverResp `json:"items"`
}

type toolListResp struct {
	Total int `json:"total"`
	Items []struct {
		ExposedName string          `json:"exposedName"`
		Name        string          `json:"name"`
		ServerKey   string          `json:"serverKey"`
		Category    string          `json:"category"`
		Available   bool            `json:"available"`
		Enabled     bool            `json:"enabled"`
		Pending     bool            `json:"pending"`
		InputSchema json.RawMessage `json:"inputSchema"`
	} `json:"items"`
}

// aggToolCount 取"AI 客户端此刻能看到几个工具"。管理列表含未暴露的工具，
// 所以它不等于 len(items)——判断"有没有真的撤下来"必须看这个数。
func (e *env) aggToolCount(t *testing.T) int {
	t.Helper()
	var conn struct {
		ToolCount int `json:"toolCount"`
	}
	e.get("/api/connect", &conn)
	return conn.ToolCount
}

type logListResp struct {
	Total int `json:"total"`
	Items []struct {
		ID          string `json:"id"`
		ServerKey   string `json:"serverKey"`
		ToolName    string `json:"toolName"`
		ExposedName string `json:"exposedName"`
		DurationMS  int64  `json:"durationMs"`
		Status      string `json:"status"`
		ErrorKind   string `json:"errorKind"`
	} `json:"items"`
}

type overviewResp struct {
	Ready   bool `json:"ready"`
	Servers struct {
		Total          int `json:"total"`
		Enabled        int `json:"enabled"`
		Disabled       int `json:"disabled"`
		Healthy        int `json:"healthy"`
		Offline        int `json:"offline"`
		Unknown        int `json:"unknown"`
		EnabledNoTools int `json:"enabledNoTools"`
	} `json:"servers"`
	Tools struct {
		Total int `json:"total"`
	} `json:"tools"`
	Calls struct {
		Total       int64   `json:"total"`
		Failed      int64   `json:"failed"`
		FailureRate float64 `json:"failureRate"`
	} `json:"calls"`
	Endpoints map[string]string `json:"endpoints"`
}

// --- 主流程 ---

// 这条用例就是验收标准里的"curl 全流程可用"：
// 增 → 测 → 查 → 启停 → 聚合 → 日志 → 概览 → 删。
func TestServerLifecycle(t *testing.T) {
	e := newEnv(t, httpd.Options{})
	ds := startDownstream(t, "alpha", "beta")

	var created serverResp
	e.mustJSON(http.MethodPost, "/api/servers?probe=1", map[string]any{
		"key":        "ds",
		"transport":  "streamable_http",
		"url":        ds,
		"timeoutMs":  5000,
		"categoryId": e.mustCategory(),
	}, http.StatusCreated, &created)

	if created.ID == "" {
		t.Fatal("创建后没有返回 id")
	}
	if created.Probe == nil {
		t.Fatal("?probe=1 应当在同一次往返里带回探活结果")
	}
	if !created.Probe.OK {
		t.Fatalf("探活失败: %s / %s", created.Probe.Kind, created.Probe.Detail)
	}
	if created.Probe.ToolCount != 2 {
		t.Errorf("探活工具数 = %d, 期望 2", created.Probe.ToolCount)
	}
	if created.Probe.ProtocolVersion == "" {
		t.Error("探活结果缺少协商到的协议版本")
	}
	if created.Status != "healthy" {
		t.Errorf("状态 = %q, 期望 healthy", created.Status)
	}
	// 新探活发现的工具默认禁用：缓存里有 2 个，但开启前不暴露。
	if created.ToolCount != 2 || created.ExposedToolCount != 0 {
		t.Errorf("工具计数 = %d/%d, 期望 2/0（默认禁用）", created.ToolCount, created.ExposedToolCount)
	}

	// 列表页要能看到它，且列出的是下游原始工具名。
	var list serverListResp
	e.get("/api/servers", &list)
	if list.Total != 1 || list.Items[0].Key != "ds" {
		t.Fatalf("列表 = %+v", list)
	}

	// 模拟管理员逐个人工启用（默认禁用是产品语义，不是缺陷）。
	e.enableAllTools()

	// 聚合工具列表：对外名必须带前缀。
	var tools toolListResp
	e.get("/api/tools", &tools)
	if tools.Total != 2 {
		t.Fatalf("聚合工具数 = %d, 期望 2", tools.Total)
	}
	seen := map[string]bool{}
	for _, item := range tools.Items {
		seen[item.ExposedName] = true
		if item.ServerKey != "ds" {
			t.Errorf("工具 %s 的来源 key 错误: %q", item.ExposedName, item.ServerKey)
		}
		if !item.Available {
			t.Errorf("工具 %s 应标记为可用", item.ExposedName)
		}
	}
	for _, want := range []string{"ds__alpha", "ds__beta"} {
		if !seen[want] {
			t.Errorf("聚合列表缺少 %q（实际 %v）", want, seen)
		}
	}

	// 停用：server 整体从聚合视图摘掉，AI 侧一个工具都看不到。
	// 管理列表（/api/tools）与 AI 侧（/api/connect 的 toolCount）是两回事：
	// 前者含未启用的工具（否则禁用就成了没有出口的黑洞），后者只数真正下发的。
	var disabled serverResp
	e.mustJSON(http.MethodPost, "/api/servers/"+created.ID+"/disable", nil, http.StatusOK, &disabled)
	if disabled.Enabled {
		t.Error("停用后 enabled 仍为 true")
	}
	if n := e.aggToolCount(t); n != 0 {
		t.Errorf("停用后 AI 可见工具数 = %d, 期望 0", n)
	}

	// 停用项不进健康分桶：status 停在停用前的 healthy（探活在停用后停止，
	// 冻结值永远不会再刷新），概览把它数进 healthy 会与真探活的混在一起。
	var ovAfterDisable overviewResp
	e.get("/api/overview", &ovAfterDisable)
	if ovAfterDisable.Servers.Disabled != 1 || ovAfterDisable.Servers.Healthy != 0 {
		t.Errorf("停用后概览 = %+v, 期望 disabled=1、healthy=0", ovAfterDisable.Servers)
	}

	// 启用：server 恢复，但工具保持禁用——停用时的级联禁用不自动回滚，
	// 哪些工具该回来由人工重新决定。管理列表仍看得见它们（Enabled=false），
	// 好让人有地方点回来；AI 侧依旧是 0。
	var enabled serverResp
	e.mustJSON(http.MethodPost, "/api/servers/"+created.ID+"/enable", nil, http.StatusOK, &enabled)
	if !enabled.Enabled {
		t.Error("启用后 enabled 为 false")
	}
	if n := e.aggToolCount(t); n != 0 {
		t.Errorf("重新启用后 AI 可见工具数 = %d, 期望 0（工具需人工重新启用）", n)
	}
	e.get("/api/tools", &tools)
	if tools.Total != 2 {
		t.Fatalf("重新启用后管理列表工具数 = %d, 期望 2（应可见但禁用）", tools.Total)
	}
	for _, item := range tools.Items {
		if item.Enabled {
			t.Errorf("工具 %s 应保持禁用：级联禁用不随 server 重新启用自动回滚", item.Name)
		}
	}
	// "启用中但 0 生效工具"是空转状态，概览必须把它数出来供告警展示。
	e.get("/api/overview", &ovAfterDisable)
	if ovAfterDisable.Servers.EnabledNoTools != 1 {
		t.Errorf("重新启用后概览 enabledNoTools = %d, 期望 1（无任何生效工具的启用 server）", ovAfterDisable.Servers.EnabledNoTools)
	}
	e.enableAllTools()
	if n := e.aggToolCount(t); n != 2 {
		t.Errorf("人工重新启用后 AI 可见工具数 = %d, 期望 2", n)
	}
	e.get("/api/overview", &ovAfterDisable)
	if ovAfterDisable.Servers.EnabledNoTools != 0 {
		t.Errorf("工具重新启用后概览 enabledNoTools = %d, 期望 0", ovAfterDisable.Servers.EnabledNoTools)
	}

	// 单点测试与刷新工具。
	var probed probeResp
	e.mustJSON(http.MethodPost, "/api/servers/"+created.ID+"/test", nil, http.StatusOK, &probed)
	if !probed.OK || probed.ToolCount != 2 {
		t.Fatalf("单点测试失败: %+v", probed)
	}
	if len(probed.Exposed) != 2 || probed.Exposed[0] != "ds__alpha" {
		t.Errorf("探活结果里应带上带前缀的对外名: %v", probed.Exposed)
	}

	var refreshed struct {
		OK        bool     `json:"ok"`
		ToolCount int      `json:"toolCount"`
		Exposed   []string `json:"exposedTools"`
	}
	e.mustJSON(http.MethodPost, "/api/servers/"+created.ID+"/refresh-tools", nil, http.StatusOK, &refreshed)
	if !refreshed.OK || refreshed.ToolCount != 2 || len(refreshed.Exposed) != 2 {
		t.Errorf("刷新工具结果异常: %+v", refreshed)
	}

	// 全量测试。
	var report struct {
		Total   int `json:"total"`
		Healthy int `json:"healthy"`
		Failed  int `json:"failed"`
		ToolNum int `json:"toolNum"`
	}
	e.mustJSON(http.MethodPost, "/api/servers/test-all", nil, http.StatusOK, &report)
	if report.Total != 1 || report.Healthy != 1 || report.ToolNum != 2 {
		t.Errorf("全量测试报告异常: %+v", report)
	}

	// 概览。
	var ov overviewResp
	e.get("/api/overview", &ov)
	if ov.Servers.Total != 1 || ov.Servers.Healthy != 1 {
		t.Errorf("概览中的 server 统计异常: %+v", ov.Servers)
	}
	if ov.Tools.Total != 2 {
		t.Errorf("概览工具数 = %d, 期望 2", ov.Tools.Total)
	}
	if !strings.HasSuffix(ov.Endpoints["mcp"], "/mcp") {
		t.Errorf("概览里的接入端点异常: %v", ov.Endpoints)
	}

	// 删除。
	resp, _ := e.call(http.MethodDelete, "/api/servers/"+created.ID, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("删除返回 %d, 期望 204", resp.StatusCode)
	}
	e.get("/api/servers", &list)
	if list.Total != 0 {
		t.Errorf("删除后仍有 %d 个 server", list.Total)
	}

	// 删掉的 id 再查要 404 而不是 500。
	resp, _ = e.call(http.MethodGet, "/api/servers/"+created.ID, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("查询已删除的 server = %d, 期望 404", resp.StatusCode)
	}
}

// 干跑探活：配置尚未落库也能拿到工具清单，且不留下任何存储痕迹。
//
// 新增向导的既定流程是「先探活 → 拿 tools/list → 勾选暴露」，
// 干跑端点是这条流程的前提。
func TestProbeDraftBeforeCreate(t *testing.T) {
	e := newEnv(t, httpd.Options{})
	ds := startDownstream(t, "alpha", "beta")

	req := map[string]any{
		"key":       "draft",
		"transport": "streamable_http",
		"url":       ds,
		"timeoutMs": 5000,
	}
	var probe probeResp
	e.mustJSON(http.MethodPost, "/api/servers/probe", req, http.StatusOK, &probe)
	if !probe.OK || probe.ToolCount != 2 {
		t.Fatalf("干跑探活失败: kind=%s detail=%s", probe.Kind, probe.Detail)
	}
	if len(probe.Exposed) != 2 || probe.Exposed[0] != "draft__alpha" {
		t.Errorf("干跑结果应带 key 前缀的对外名: %v", probe.Exposed)
	}

	// 干跑不落库：探活成功也不产生 server 记录，聚合端点上也没有工具。
	var list serverListResp
	e.get("/api/servers", &list)
	if list.Total != 0 {
		t.Errorf("干跑探活不应落库，却发现了 %d 个 server", list.Total)
	}
	var tools toolListResp
	e.get("/api/tools", &tools)
	if tools.Total != 0 {
		t.Errorf("干跑探活不应产生聚合工具，却发现了 %d 个", tools.Total)
	}

	// 配置不合法直接 400，错误信息指向配置本身。
	resp, raw := e.call(http.MethodPost, "/api/servers/probe", map[string]any{
		"key": "bad", "transport": "streamable_http",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("非法配置 = %d, 期望 400\n响应: %s", resp.StatusCode, raw)
	}
}

// 管理接口说"已聚合"，AI 客户端就必须看得到——否则界面只是个装饰。
func TestMCPEndpointReflectsAdminChanges(t *testing.T) {
	e := newEnv(t, httpd.Options{})
	ds := startDownstream(t, "ping")

	e.mustJSON(http.MethodPost, "/api/servers?probe=1", map[string]any{
		"key": "live", "transport": "streamable_http", "url": ds,
		"categoryId": e.mustCategory(),
	}, http.StatusCreated, nil)

	// 新工具默认禁用：启用后 AI 才看得到。
	e.enableAllTools()

	c := e.mcpClient()
	ctx := context.Background()

	res, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		t.Fatalf("tools/list 失败: %v", err)
	}
	if len(res.Tools) != 1 || res.Tools[0].Name != "live__ping" {
		t.Fatalf("AI 看到的工具 = %v", res.Tools)
	}

	// 通过管理接口停用，AI 的工具列表应立即收缩。
	var list serverListResp
	e.get("/api/servers", &list)
	e.mustJSON(http.MethodPost, "/api/servers/"+list.Items[0].ID+"/disable", nil, http.StatusOK, nil)

	res, err = c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		t.Fatalf("停用后 tools/list 失败: %v", err)
	}
	if len(res.Tools) != 0 {
		t.Errorf("停用后 AI 仍能看到 %d 个工具", len(res.Tools))
	}
}

// --- 密钥 ---

// 密钥在回显时必须打码，但用户不改它时不能真的被写坏。
//
// 这条最容易出事：每编辑一次配置就抹掉一次凭据，而界面上一切正常，
// 直到下游开始返回 401。
func TestSecretMaskingAndPreservation(t *testing.T) {
	e := newEnv(t, httpd.Options{})
	ds := startDownstream(t, "one")

	token := "Bearer super-secret-token"
	e.mustJSON(http.MethodPost, "/api/servers", map[string]any{
		"key":        "sec",
		"transport":  "streamable_http",
		"url":        ds,
		"headers":    map[string]string{"Authorization": token},
		"env":        []string{"API_KEY=env-secret", "PLAIN=visible"},
		"categoryId": e.mustCategory(),
	}, http.StatusCreated, nil)

	var list serverListResp
	e.get("/api/servers", &list)
	id := list.Items[0].ID

	var detail serverResp
	e.get("/api/servers/"+id, &detail)
	if got := detail.Config.Headers["Authorization"]; got != "***" {
		t.Errorf("回显的凭据 = %q, 期望打码", got)
	}
	if len(detail.Config.HeaderKeys) != 1 || detail.Config.HeaderKeys[0] != "Authorization" {
		t.Errorf("应保留键名供用户辨认，得到 %v", detail.Config.HeaderKeys)
	}
	for _, kv := range detail.Config.Env {
		if strings.HasPrefix(kv, "API_KEY=") && kv != "API_KEY=***" {
			t.Errorf("env 值未打码: %q", kv)
		}
	}

	// 原样提交打码值 → 库里必须还是真令牌。
	e.mustJSON(http.MethodPut, "/api/servers/"+id, map[string]any{
		"key":       "sec",
		"transport": "streamable_http",
		"url":       ds,
		"headers":   detail.Config.Headers,
		"env":       detail.Config.Env,
	}, http.StatusOK, nil)

	saved, err := e.store.Servers.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("回读失败: %v", err)
	}
	if got := saved.Spec.Headers["Authorization"]; got != token {
		t.Errorf("提交打码值后凭据被打坏了: %q", got)
	}
	if got := strings.Join(saved.Spec.Env, ","); !strings.Contains(got, "API_KEY=env-secret") {
		t.Errorf("env 里的密钥被打坏了: %q", got)
	}

	// 提交新值 → 应当被采纳。
	e.mustJSON(http.MethodPut, "/api/servers/"+id, map[string]any{
		"key":       "sec",
		"transport": "streamable_http",
		"url":       ds,
		"headers":   map[string]string{"Authorization": "Bearer rotated"},
	}, http.StatusOK, nil)

	saved, _ = e.store.Servers.Get(context.Background(), id)
	if got := saved.Spec.Headers["Authorization"]; got != "Bearer rotated" {
		t.Errorf("新凭据未被采纳: %q", got)
	}
}

// --- 校验与冲突 ---

func TestValidationErrorsAreBadRequest(t *testing.T) {
	e := newEnv(t, httpd.Options{})

	cases := []struct {
		name string
		body map[string]any
		want string
	}{
		{"缺 url", map[string]any{"key": "k", "transport": "streamable_http"}, "invalid_request"},
		{"未知传输", map[string]any{"key": "k", "transport": "carrier-pigeon", "url": "http://x/mcp"}, "invalid_request"},
		{"非法 key", map[string]any{"key": "bad key!", "transport": "streamable_http", "url": "http://x/mcp"}, "invalid_request"},
		{"allow 模式空名单", map[string]any{
			"key": "k", "transport": "streamable_http", "url": "http://x/mcp",
			"toolFilterMode": "allow",
		}, "invalid_request"},
		{"未知字段", map[string]any{
			"key": "k", "transport": "streamable_http", "url": "http://x/mcp", "typoField": 1,
		}, "invalid_request"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, raw := e.call(http.MethodPost, "/api/servers", tc.body)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("状态码 = %d, 期望 400\n%s", resp.StatusCode, raw)
			}
			var body struct {
				Code  string `json:"code"`
				Error string `json:"error"`
			}
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Fatalf("错误体不是 JSON: %s", raw)
			}
			if body.Code != tc.want {
				t.Errorf("code = %q, 期望 %q", body.Code, tc.want)
			}
			if body.Error == "" {
				t.Error("错误体缺少可读说明")
			}
		})
	}
}

// key="hub" 与网关自带工具共用一个命名空间：对外名是 <serverKey>__<toolName>，
// 而元工具统一以 hub__ 开头。key 取 hub 会让这台 server 的全部工具冒充成元
// 工具出现在 meta 模式的 tools/list 里，真撞上同名的那几个（hub__list_tools
// 等）连调用指向哪边都说不清。命名空间是保留的，保存时就该拒绝，
// 而不是等 AI 反馈"工具怪怪的"。
func TestReservedServerKeyIsRejected(t *testing.T) {
	e := newEnv(t, httpd.Options{})

	resp, raw := e.call(http.MethodPost, "/api/servers", map[string]any{
		"key": "hub", "transport": "streamable_http", "url": "http://x/mcp",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("key=hub 状态码 = %d, 期望 400\n%s", resp.StatusCode, raw)
	}
	var body struct {
		Code  string `json:"code"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("错误体不是 JSON: %s", raw)
	}
	if body.Code != "invalid_request" || !strings.Contains(body.Error, "保留命名空间") {
		t.Errorf("错误体应点明保留命名空间: code=%q error=%q", body.Code, body.Error)
	}

	// 对照：非保留 key 不该被这条规则拦下（它会继续走到"必须指定分类"那步）。
	_, raw2 := e.call(http.MethodPost, "/api/servers", map[string]any{
		"key": "hubx", "transport": "streamable_http", "url": "http://x/mcp",
	})
	if strings.Contains(string(raw2), "保留命名空间") {
		t.Errorf("key=hubx 被误判成保留名: %s", raw2)
	}
}

// 新建 server 必须指定分类：分类是组织单元、检索过滤与 facet 的来源，
// 但**不进对外工具名**。缺分类时的提示要带回候选，界面就地可选。
// 分类不存在同样是 400，否则 category_id 会留下悬空引用。
func TestCreateServerRequiresCategory(t *testing.T) {
	e := newEnv(t, httpd.Options{})
	ds := startDownstream(t, "x")

	resp, raw := e.call(http.MethodPost, "/api/servers", map[string]any{
		"key": "nocat", "transport": "streamable_http", "url": ds,
	})
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(raw), "分类") {
		t.Fatalf("缺分类应 400，实际 %d\n%s", resp.StatusCode, raw)
	}

	resp, raw = e.call(http.MethodPost, "/api/servers", map[string]any{
		"key": "nocat", "transport": "streamable_http", "url": ds, "categoryId": "cat_ghost",
	})
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(raw), "不存在") {
		t.Fatalf("不存在的分类应 400，实际 %d\n%s", resp.StatusCode, raw)
	}
}

func TestDuplicateKeyIsConflict(t *testing.T) {
	e := newEnv(t, httpd.Options{})
	ds := startDownstream(t, "x")

	body := map[string]any{"key": "dup", "transport": "streamable_http", "url": ds, "categoryId": e.mustCategory()}
	e.mustJSON(http.MethodPost, "/api/servers", body, http.StatusCreated, nil)

	// 同名再建：409 而不是悄悄覆盖。否则用户以为新建了一个，
	// 实际上改掉了正在服务的那个。
	resp, raw := e.call(http.MethodPost, "/api/servers", body)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("重复 key = %d, 期望 409\n%s", resp.StatusCode, raw)
	}
	if !strings.Contains(string(raw), "upsert") {
		t.Errorf("409 应当告诉用户怎么强制覆盖: %s", raw)
	}

	// 显式 upsert 才允许覆盖。
	e.mustJSON(http.MethodPost, "/api/servers?upsert=1", body, http.StatusCreated, nil)

	var list serverListResp
	e.get("/api/servers", &list)
	if list.Total != 1 {
		t.Errorf("覆盖后 server 数 = %d, 期望 1", list.Total)
	}
}

// PUT 改 key 是原地更新，不是新建：改的是同一行的前缀。
//
// 新建要校验分类（PRD §4.7.3），更新不要——否则"改个 key"会被
// "新建 server 必须指定分类"挡住：请求体里没带分类的编辑根本提交不了。
func TestUpdateServerKeyChangeIsUpdate(t *testing.T) {
	e := newEnv(t, httpd.Options{})
	ds := startDownstream(t, "x")

	var created serverResp
	e.mustJSON(http.MethodPost, "/api/servers", map[string]any{
		"key": "oldkey", "transport": "streamable_http", "url": ds,
		"categoryId": e.mustCategory(),
	}, http.StatusCreated, &created)

	// 改 key，请求体不带分类：嵌入未启用时，走新建路径必然被拒。
	var updated serverResp
	e.mustJSON(http.MethodPut, "/api/servers/"+created.ID, map[string]any{
		"key": "newkey", "transport": "streamable_http", "url": ds,
	}, http.StatusOK, &updated)
	if updated.Key != "newkey" {
		t.Errorf("回显的 key = %q, 期望 newkey", updated.Key)
	}
	if updated.ID != created.ID {
		t.Errorf("改 key 应原地更新同一行: id=%s want %s", updated.ID, created.ID)
	}

	// 新 key 可查，老 key 查不到，且没有多出一条。
	saved, err := e.store.Servers.GetByKey(context.Background(), "newkey")
	if err != nil {
		t.Fatalf("新 key 查不到: %v", err)
	}
	if saved.Spec.ID != created.ID {
		t.Errorf("新 key 应指向同一条记录: id=%s want %s", saved.Spec.ID, created.ID)
	}
	if _, err := e.store.Servers.GetByKey(context.Background(), "oldkey"); err == nil {
		t.Errorf("老 key 应已被换掉")
	}

	var list serverListResp
	e.get("/api/servers", &list)
	if list.Total != 1 {
		t.Errorf("改 key 后 server 数 = %d, 期望 1", list.Total)
	}
}

// 更新时给了不存在的分类必须 400，且原记录不能被写坏。
//
// 分类的存在性以前只在新建路径校验，更新路径不校验：写进去的是悬空
// 引用，界面上那一列显示成空白分类。校验要发生在落库之前。
func TestUpdateServerRejectsUnknownCategory(t *testing.T) {
	e := newEnv(t, httpd.Options{})
	ds := startDownstream(t, "x")
	catID := e.mustCategory()

	var created serverResp
	e.mustJSON(http.MethodPost, "/api/servers", map[string]any{
		"key": "srv", "transport": "streamable_http", "url": ds, "categoryId": catID,
	}, http.StatusCreated, &created)

	resp, raw := e.call(http.MethodPut, "/api/servers/"+created.ID, map[string]any{
		"key": "srv2", "transport": "streamable_http", "url": ds, "categoryId": "cat_ghost",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("不存在的分类应 400，实际 %d\n%s", resp.StatusCode, raw)
	}
	if !strings.Contains(string(raw), "不存在") {
		t.Errorf("错误体应说明分类不存在: %s", raw)
	}

	// 原记录没被写坏：分类还在，key 也没被这次失败的写入改掉。
	saved, err := e.store.Servers.Get(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("读 server 失败: %v", err)
	}
	if saved.CategoryID != catID {
		t.Errorf("分类 = %q, 期望 %q（失败的写入不该改分类）", saved.CategoryID, catID)
	}
	if saved.Spec.Key != "srv" {
		t.Errorf("失败的写入不该改 key: =%q", saved.Spec.Key)
	}
}

// 编辑请求不带 categoryId 时，原分类要留着。
//
// "没提"不等于"清空"：请求体没带的字段应沿用旧值（密钥掩码就是这么
// 还原的），分类同理。
func TestUpdateServerKeepsCategoryWhenOmitted(t *testing.T) {
	e := newEnv(t, httpd.Options{})
	ds := startDownstream(t, "x")
	catID := e.mustCategory()

	var created serverResp
	e.mustJSON(http.MethodPost, "/api/servers", map[string]any{
		"key": "srv", "transport": "streamable_http", "url": ds, "categoryId": catID,
	}, http.StatusCreated, &created)

	var updated serverResp
	e.mustJSON(http.MethodPut, "/api/servers/"+created.ID, map[string]any{
		"key": "srv", "transport": "streamable_http", "url": ds,
	}, http.StatusOK, &updated)

	saved, err := e.store.Servers.Get(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("读 server 失败: %v", err)
	}
	if saved.CategoryID != catID {
		t.Errorf("分类 = %q, 期望 %q（没带 categoryId 不该抹掉原分类）", saved.CategoryID, catID)
	}
}

// 显式给空串才是"清空分类"：与"请求体没带这个字段"是两回事。
func TestUpdateServerExplicitlyClearsCategory(t *testing.T) {
	e := newEnv(t, httpd.Options{})
	ds := startDownstream(t, "x")
	catID := e.mustCategory()

	var created serverResp
	e.mustJSON(http.MethodPost, "/api/servers", map[string]any{
		"key": "srv", "transport": "streamable_http", "url": ds, "categoryId": catID,
	}, http.StatusCreated, &created)

	var updated serverResp
	e.mustJSON(http.MethodPut, "/api/servers/"+created.ID, map[string]any{
		"key": "srv", "transport": "streamable_http", "url": ds, "categoryId": "",
	}, http.StatusOK, &updated)

	saved, err := e.store.Servers.Get(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("读 server 失败: %v", err)
	}
	if saved.CategoryID != "" {
		t.Errorf("分类 = %q, 期望空（显式清空后应是未分类）", saved.CategoryID)
	}
}

// 覆盖写已有 key（?upsert=1）且没带 categoryId 时，已有分类不该被抹掉，
// 也不该被写成悬空引用。
func TestUpsertExistingServerKeepsCategory(t *testing.T) {
	e := newEnv(t, httpd.Options{})
	ds := startDownstream(t, "x")
	catID := e.mustCategory()

	e.mustJSON(http.MethodPost, "/api/servers", map[string]any{
		"key": "srv", "transport": "streamable_http", "url": ds, "categoryId": catID,
	}, http.StatusCreated, nil)

	// 覆盖写：请求不带分类。
	e.mustJSON(http.MethodPost, "/api/servers?upsert=1", map[string]any{
		"key": "srv", "transport": "streamable_http", "url": ds,
	}, http.StatusCreated, nil)

	saved, err := e.store.Servers.GetByKey(context.Background(), "srv")
	if err != nil {
		t.Fatalf("读 server 失败: %v", err)
	}
	if saved.CategoryID != catID {
		t.Errorf("分类 = %q, 期望 %q（覆盖写没带分类不该抹掉原分类）", saved.CategoryID, catID)
	}
}

// 覆盖写已有 key（?upsert=1）时，请求没带的 headers / env / enabled /
// autoEnable 都该沿用原值；只有显式给了才覆盖。否则一次只改 URL 的
// 覆盖写会把凭据与启停状态悄悄清掉。
func TestUpsertExistingServerKeepsUnsetFields(t *testing.T) {
	e := newEnv(t, httpd.Options{})
	ds := startDownstream(t, "x")
	catID := e.mustCategory()

	e.mustJSON(http.MethodPost, "/api/servers", map[string]any{
		"key": "srv", "transport": "streamable_http", "url": ds, "categoryId": catID,
		"headers":    map[string]string{"Authorization": "Bearer secret"},
		"env":        []string{"TOKEN=secret"},
		"autoEnable": true,
		"enabled":    false,
	}, http.StatusCreated, nil)

	e.mustJSON(http.MethodPost, "/api/servers?upsert=1", map[string]any{
		"key": "srv", "transport": "streamable_http", "url": ds,
	}, http.StatusCreated, nil)

	saved, err := e.store.Servers.GetByKey(context.Background(), "srv")
	if err != nil {
		t.Fatalf("读 server 失败: %v", err)
	}
	if got := saved.Spec.Headers["Authorization"]; got != "Bearer secret" {
		t.Errorf("Authorization = %q, 期望保留原凭据", got)
	}
	if len(saved.Spec.Env) != 1 || saved.Spec.Env[0] != "TOKEN=secret" {
		t.Errorf("Env = %v, 期望保留原环境变量", saved.Spec.Env)
	}
	if !saved.AutoEnable {
		t.Error("autoEnable 被覆盖写清掉了")
	}
	if saved.Enabled {
		t.Error("enabled 被覆盖写改成了启用")
	}
}

// --- 鉴权 ---

func TestAPIKeyProtectsAdminEndpoints(t *testing.T) {
	e := newEnv(t, httpd.Options{APIKey: "s3cret"})

	// 无凭据。
	resp, _ := e.call(http.MethodGet, "/api/overview", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("无凭据访问 = %d, 期望 401", resp.StatusCode)
	}

	// 错误凭据。
	resp, _ = e.callH(http.MethodGet, "/api/overview", nil,
		map[string]string{"X-API-Key": "wrong"})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("错误凭据 = %d, 期望 401", resp.StatusCode)
	}

	// 正确凭据：两种携带方式都认。
	resp, _ = e.callH(http.MethodGet, "/api/overview", nil,
		map[string]string{"X-API-Key": "s3cret"})
	if resp.StatusCode != http.StatusOK {
		t.Errorf("X-API-Key = %d, 期望 200", resp.StatusCode)
	}
	resp, _ = e.callH(http.MethodGet, "/api/overview", nil,
		map[string]string{"Authorization": "Bearer s3cret"})
	if resp.StatusCode != http.StatusOK {
		t.Errorf("Bearer = %d, 期望 200", resp.StatusCode)
	}

	// 写操作同样受保护：漏掉一个端点就等于没有鉴权。
	resp, _ = e.call(http.MethodPost, "/api/servers", map[string]any{
		"key": "k", "transport": "streamable_http", "url": "http://127.0.0.1:1/mcp",
	})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("无凭据写操作 = %d, 期望 401", resp.StatusCode)
	}

	// 健康检查必须免鉴权，否则探针会一直失败。
	resp, _ = e.call(http.MethodGet, "/healthz", nil)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("/healthz = %d, 期望 200（探针不该被鉴权挡住）", resp.StatusCode)
	}
}

// --- 创建引导名单 ---

func TestBootEnabledToolsThroughAPI(t *testing.T) {
	e := newEnv(t, httpd.Options{})
	ds := startDownstream(t, "keep", "drop")

	// 创建时带上向导勾选的初始启用集合：keep 进聚合，drop 保持禁用。
	e.mustJSON(http.MethodPost, "/api/servers?probe=1", map[string]any{
		"key": "f", "transport": "streamable_http", "url": ds,
		"enabledTools": []string{"keep"},
		"categoryId":   e.mustCategory(),
	}, http.StatusCreated, nil)

	var list serverListResp
	e.get("/api/servers", &list)
	if list.Items[0].ToolCount != 2 || list.Items[0].ExposedToolCount != 1 {
		t.Errorf("工具计数 = %d/%d, 期望 2/1（缓存 2 个、聚合 1 个）",
			list.Items[0].ToolCount, list.Items[0].ExposedToolCount)
	}

	var tools toolListResp
	e.get("/api/tools?serverKey=f", &tools)
	// 管理列表里两个都在：没启用的那个必须仍然看得见、能操作，
	// 否则"它为什么不生效"无从查起。是否在聚合里，看 enabled。
	if tools.Total != 2 {
		t.Fatalf("工具列表条数 = %d, 期望 2（未启用的也要留在管理视图里）", tools.Total)
	}
	enabledByName := map[string]bool{}
	for _, it := range tools.Items {
		enabledByName[it.Name] = it.Enabled
	}
	if !enabledByName["keep"] || enabledByName["drop"] {
		t.Errorf("启用标记 = %v, 期望只有 keep 启用", enabledByName)
	}
	// AI 那侧：只有 1 个。
	if n := e.aggToolCount(t); n != 1 {
		t.Errorf("AI 可见工具数 = %d, 期望 1", n)
	}
}

// --- 自动启用开关（O5） ---

// 开了开关的 server：上游新增的工具不经人工确认就放行，但每一条都留痕，
// 且开关本身在响应里看得见——"哪些上游我信过"必须是可回查的。
func TestAutoEnableToolsThroughAPI(t *testing.T) {
	e := newEnv(t, httpd.Options{})
	ds := startDownstream(t, "alpha", "beta")

	e.mustJSON(http.MethodPost, "/api/servers?probe=1", map[string]any{
		"key": "ae", "transport": "streamable_http", "url": ds,
		"autoEnable": true,
		"categoryId": e.mustCategory(),
	}, http.StatusCreated, nil)

	var list serverListResp
	e.get("/api/servers", &list)
	if len(list.Items) != 1 {
		t.Fatalf("server 数 = %d, want 1", len(list.Items))
	}
	if !list.Items[0].AutoEnable {
		t.Error("列表未回显 autoEnable=true：界面无从看出这个 server 开着自动放行")
	}
	if list.Items[0].ToolCount != 2 || list.Items[0].ExposedToolCount != 2 {
		t.Errorf("工具计数 = %d/%d, 期望 2/2（自动启用后直接暴露）",
			list.Items[0].ToolCount, list.Items[0].ExposedToolCount)
	}
	if n := e.aggToolCount(t); n != 2 {
		t.Errorf("AI 可见工具数 = %d, 期望 2", n)
	}

	// 没有待人工处理：自动放行的工具不该再出现在"待启用"告警里，
	// 否则卡片会一直喊人去做一件已经做完的事。
	var ov struct {
		Tools struct {
			PendingEnable int64 `json:"pendingEnable"`
		} `json:"tools"`
	}
	e.get("/api/overview", &ov)
	if ov.Tools.PendingEnable != 0 {
		t.Errorf("待启用数 = %d, 期望 0（已自动启用）", ov.Tools.PendingEnable)
	}

	// 关掉开关：只影响此后新发现的工具，已经启用的保持原样。
	id := list.Items[0].ID
	var updated serverResp
	e.mustJSON(http.MethodPut, "/api/servers/"+id, map[string]any{
		"key": "ae", "transport": "streamable_http", "url": ds,
		"autoEnable": false,
		"categoryId": e.mustCategory(),
	}, http.StatusOK, &updated)
	if updated.AutoEnable {
		t.Error("关闭开关后响应仍回 true")
	}
	e.get("/api/servers/"+id, &updated)
	if updated.AutoEnable {
		t.Error("关闭开关后回读仍为 true：编辑没有落库")
	}
	if n := e.aggToolCount(t); n != 2 {
		t.Errorf("关掉开关不该撤下已启用的工具，AI 可见 = %d, 期望 2", n)
	}
}

// --- 故障下游 ---

// 一个连不上的下游，不该让管理接口或聚合端点整体失效。
func TestDeadDownstreamIsReportedNotFatal(t *testing.T) {
	e := newEnv(t, httpd.Options{})
	good := startDownstream(t, "fine")

	e.mustJSON(http.MethodPost, "/api/servers?probe=1", map[string]any{
		"key": "good", "transport": "streamable_http", "url": good, "timeoutMs": 3000,
		"categoryId": e.mustCategory(),
	}, http.StatusCreated, nil)

	var bad serverResp
	e.mustJSON(http.MethodPost, "/api/servers?probe=1", map[string]any{
		"key": "bad", "transport": "streamable_http", "url": deadURL(t), "timeoutMs": 3000,
		"categoryId": e.mustCategory(),
	}, http.StatusCreated, &bad)

	// 健康下游的工具同样默认禁用，显式启用后才进入聚合端点。
	e.enableAllTools()

	if bad.Probe == nil || bad.Probe.OK {
		t.Fatal("连不上的下游不应报告探活成功")
	}
	if bad.Probe.Kind == "" || bad.Probe.Kind == "UNKNOWN" {
		t.Errorf("失败原因应被分类，得到 %q", bad.Probe.Kind)
	}
	if bad.Probe.Hint == "" {
		t.Error("失败结果应带上排查方向")
	}
	if bad.Status != "offline" {
		t.Errorf("状态 = %q, 期望 offline", bad.Status)
	}
	if bad.LastErrorHint == "" {
		t.Error("列表视图也应带上可读的失败说明")
	}

	// 健康的下游不受影响。
	var tools toolListResp
	e.get("/api/tools", &tools)
	if tools.Total != 1 || tools.Items[0].ExposedName != "good__fine" {
		t.Errorf("故障隔离失效，聚合列表 = %+v", tools.Items)
	}

	// 全量测试仍然返回 200 与逐项结果。
	var report struct {
		Healthy int `json:"healthy"`
		Failed  int `json:"failed"`
	}
	e.mustJSON(http.MethodPost, "/api/servers/test-all", nil, http.StatusOK, &report)
	if report.Healthy != 1 || report.Failed != 1 {
		t.Errorf("全量测试 = %+v, 期望 1 健康 1 失败", report)
	}

	// 概览照常。
	var ov overviewResp
	e.get("/api/overview", &ov)
	if ov.Servers.Offline != 1 || ov.Tools.Total != 1 {
		t.Errorf("概览 = %+v", ov)
	}

	// 聚合端点对 AI 仍然可用。
	c := e.mcpClient()
	if res, err := c.ListTools(context.Background(), mcp.ListToolsRequest{}); err != nil {
		t.Fatalf("下游故障时 tools/list 失败: %v", err)
	} else if len(res.Tools) != 1 {
		t.Errorf("工具数 = %d, 期望 1", len(res.Tools))
	}
}

// --- 调用日志 ---

func TestCallLogsFlow(t *testing.T) {
	e := newEnv(t, httpd.Options{})
	ds := startDownstream(t, "logged")

	e.mustJSON(http.MethodPost, "/api/servers?probe=1", map[string]any{
		"key": "lg", "transport": "streamable_http", "url": ds,
		"categoryId": e.mustCategory(),
	}, http.StatusCreated, nil)

	// 默认禁用，启用后才能被 AI 调用。
	e.enableAllTools()

	// 走一次真实的工具调用，日志才有内容。
	c := e.mcpClient()
	req := mcp.CallToolRequest{}
	req.Params.Name = "lg__logged"
	if _, err := c.CallTool(context.Background(), req); err != nil {
		t.Fatalf("调用工具失败: %v", err)
	}

	// 日志是异步落库的，等它冲一批。
	var logs logListResp
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		e.get("/api/logs?limit=10", &logs)
		if logs.Total > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if logs.Total == 0 {
		t.Fatal("调用没有被记入日志")
	}
	entry := logs.Items[0]
	if entry.ExposedName != "lg__logged" {
		t.Errorf("日志里的工具名 = %q", entry.ExposedName)
	}
	if entry.ToolName != "logged" {
		t.Errorf("应记录下游原始名，得到 %q", entry.ToolName)
	}
	if entry.ServerKey != "lg" {
		t.Errorf("日志里的 server key = %q", entry.ServerKey)
	}
	if entry.Status != "ok" {
		t.Errorf("日志状态 = %q, 期望 ok", entry.Status)
	}

	// 筛选：按 server 与状态。
	var filtered logListResp
	e.get("/api/logs?status=error", &filtered)
	if filtered.Total != 0 {
		t.Errorf("按 error 筛选得到 %d 条，期望 0", filtered.Total)
	}

	// 时间窗筛选：给一个未来的起点，应当没有记录。
	e.get("/api/logs?since="+strconv.FormatInt(time.Now().Add(time.Hour).UnixMilli(), 10), &filtered)
	if filtered.Total != 0 {
		t.Errorf("未来时间窗仍有 %d 条记录", filtered.Total)
	}

	// RFC3339 也要认——手敲 curl 时没人愿意换算毫秒。
	// 注意 "+08:00" 里的加号在 query 里必须转义，这是客户端的责任。
	e.get("/api/logs?since="+url.QueryEscape(time.Now().Add(-time.Hour).Format(time.RFC3339)), &filtered)
	if filtered.Total == 0 {
		t.Error("RFC3339 形式的时间参数没被识别")
	}

	// 排序参数：sort / order 透传到 store，非法值静默回退到时间倒序 ——
	// 拼错一个参数值不该把整页日志打成 400。
	e.get("/api/logs?sort=status&order=asc", &filtered)
	if filtered.Total == 0 {
		t.Errorf("按状态正序查询得到 0 条，期望仍有日志")
	}
	e.get("/api/logs?sort=bogus&order=sideways", &filtered)
	if filtered.Total == 0 {
		t.Error("非法排序参数应回退到默认序，而不是返回空结果或 400")
	}

	// 自由文本搜索 q。这里断言两端：搜得到刚调用的那次，也搜不到不存在的词。
	// 只断言"搜到了"是不够的——前端一直在发这个参数，后端整段忽略它，
	// 功能照样"看起来能用"，只是结果永远等于全量。
	e.get("/api/logs?q="+url.QueryEscape("logged"), &filtered)
	if filtered.Total == 0 {
		t.Error("q 没被解析：按工具名搜不到刚调用过的那次")
	}
	e.get("/api/logs?q="+url.QueryEscape("no-such-tool-xyz"), &filtered)
	if filtered.Total != 0 {
		t.Errorf("q 没被解析：不存在的词也搜出 %d 条", filtered.Total)
	}
	// LIKE 通配符要当字面量。不转义的话 "%%%" 匹配全表，
	// 表现成"搜索框接上了但筛不动"。
	e.get("/api/logs?q="+url.QueryEscape("%"), &filtered)
	if filtered.Total != 0 {
		t.Errorf("q=%% 被当成通配符了，得到 %d 条", filtered.Total)
	}

	// 清空。
	var cleared struct {
		Deleted int64 `json:"deleted"`
	}
	e.mustJSON(http.MethodDelete, "/api/logs", nil, http.StatusOK, &cleared)
	if cleared.Deleted == 0 {
		t.Error("清空日志应返回删除条数")
	}
	e.get("/api/logs", &logs)
	if logs.Total != 0 {
		t.Errorf("清空后仍有 %d 条日志", logs.Total)
	}
}

// --- 接入指引 ---

func TestConnectInfo(t *testing.T) {
	e := newEnv(t, httpd.Options{EnableSSE: true})
	ds := startDownstream(t, "a")
	e.mustJSON(http.MethodPost, "/api/servers?probe=1", map[string]any{
		"key": "a", "transport": "streamable_http", "url": ds,
		"categoryId": e.mustCategory(),
	}, http.StatusCreated, nil)
	e.enableAllTools()

	var out struct {
		MCP        string            `json:"mcpEndpoint"`
		SSE        string            `json:"sseEndpoint"`
		Health     string            `json:"healthEndpoint"`
		MCPClients map[string]string `json:"mcpServers"`
		ToolCount  int               `json:"toolCount"`
		Summary    string            `json:"summary"`
		Note       string            `json:"note"`
	}
	e.get("/api/connect", &out)

	if !strings.HasSuffix(out.MCP, "/mcp") {
		t.Errorf("mcpEndpoint = %q", out.MCP)
	}
	if !strings.HasSuffix(out.SSE, "/sse") {
		t.Errorf("开启 SSE 时端点应出现，得到 %q", out.SSE)
	}
	if out.MCPClients["mcp-hub"] != out.MCP {
		t.Errorf("一键复制的配置不对: %v", out.MCPClients)
	}
	if out.ToolCount != 1 || !strings.Contains(out.Summary, "1") {
		t.Errorf("接入说明与实际不符: %+v", out)
	}

	// 命名形状必须与聚合层实现同形：接入方会照 note 拼工具名去调用，
	// 文案说三段而实现是两段（分类不进名字）时，失败长得像"工具不存在"，
	// 归因不到文案写错了。用 registry.Separator 现拼，不让测试复述形状。
	if want := "<serverKey>" + registry.Separator + "<toolName>"; !strings.Contains(out.Note, want) {
		t.Errorf("note 里的命名形状应为 %q，得到 %q", want, out.Note)
	}
	if strings.Contains(out.Note, "<category>") {
		t.Errorf("分类刻意不进对外名，note 却还写着三段式: %q", out.Note)
	}
}

/* ────────────────────────────── 分类（PRD §4.7.3） ────────────────────────────── */

// 分类必须同时给中文名与英文名，两次都不合规就报错、不做静默改写。
//
// 失败面各自钉住：中文名为空是 400，英文名不合规是 400（用户输入问题，
// 界面要在表单里就地提示），中文名/英文名撞车是 409（让用户在名字上
// 做选择）。混成 500 的话，界面只能弹一句「操作失败」。
//
// 英文名那条尤其要钉死"不替换"：大写、空格、下划线都不许被折成
// 别的值——存下来的必须和用户填的一样。
func TestCategoryCRUDViaAPI(t *testing.T) {
	e := newEnv(t, httpd.Options{})

	type categoryResp struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Slug        string `json:"slug"`
		Description string `json:"description"`
		ServerCount int    `json:"serverCount"`
		ToolCount   int    `json:"toolCount"`
	}

	// 中文名为空 / 英文名缺失或不合规 → 400
	for _, body := range []map[string]any{
		{"name": "  ", "slug": "weather"},
		{"name": "天气"},
		{"name": "天气", "slug": ""},
		{"name": "天气", "slug": "天气"},
		{"name": "天气", "slug": "Weather"},               // 大写不折成小写
		{"name": "天气", "slug": "web search"},            // 空格不折成连字符
		{"name": "天气", "slug": "web_search"},            // 下划线不折成连字符
		{"name": "天气", "slug": "web-"},                  // 首尾连字符
		{"name": "天气", "slug": strings.Repeat("a", 33)}, // 超长不截断
	} {
		resp, raw := e.call(http.MethodPost, "/api/categories", body)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("分类名 %v 应被拒，实际 %d\n响应: %s", body, resp.StatusCode, raw)
		}
	}

	// 正常创建：英文名原样落库（只去掉首尾空白）
	var cat categoryResp
	e.mustJSON(http.MethodPost, "/api/categories", map[string]any{
		"name": "天气", "slug": " weather ", "description": "天气类下游",
	}, http.StatusCreated, &cat)
	if cat.Slug != "weather" {
		t.Fatalf("返回的 slug = %q, 期望原样的 weather", cat.Slug)
	}

	// 中文名撞车 → 409
	resp, raw := e.call(http.MethodPost, "/api/categories", map[string]any{
		"name": "天气", "slug": "weather2",
	})
	if resp.StatusCode != http.StatusConflict || !strings.Contains(string(raw), "category_taken") {
		t.Fatalf("重名应 409 category_taken，实际 %d\n响应: %s", resp.StatusCode, raw)
	}

	// 英文名也判重 → 409（与中文名共用 category_taken 错误码，
	// 但响应体文案点明是英文名撞车）
	resp, raw = e.call(http.MethodPost, "/api/categories", map[string]any{
		"name": "气象", "slug": "weather",
	})
	if resp.StatusCode != http.StatusConflict || !strings.Contains(string(raw), "英文名") {
		t.Fatalf("英文名重名应 409，实际 %d\n响应: %s", resp.StatusCode, raw)
	}

	// 编辑：改英文名要落到返回体上
	var updated categoryResp
	e.mustJSON(http.MethodPut, "/api/categories/"+cat.ID, map[string]any{
		"name": "天气预报", "slug": "forecast", "description": "天气类下游",
	}, http.StatusOK, &updated)
	if updated.Name != "天气预报" || updated.Slug != "forecast" {
		t.Fatalf("编辑结果 = %+v", updated)
	}

	// 列表带 server / tool 计数（0 也要给出来，不能省略成缺字段）
	var list struct {
		Total int            `json:"total"`
		Items []categoryResp `json:"items"`
	}
	e.get("/api/categories", &list)
	if list.Total != 1 {
		t.Fatalf("分类数 = %d, 期望 1", list.Total)
	}
	for _, item := range list.Items {
		if item.ServerCount != 0 || item.ToolCount != 0 {
			t.Errorf("空分类的计数应为 0: %+v", item)
		}
	}

	// 删除：返回 204，且不存在的 id 是 404
	e.mustJSON(http.MethodDelete, "/api/categories/"+cat.ID, nil, http.StatusNoContent, nil)
	e.mustJSON(http.MethodDelete, "/api/categories/"+cat.ID, nil, http.StatusNotFound, nil)
}

// 分类的任何改动都不该碰对外工具名：名字里只有 serverKey 与下游工具名。
//
// 这条是"改分类不打断调用方"的守门测试——分类曾经是工具名的第一段，
// 改一次 slug 就等于对所有 AI 客户端做一次破坏性重命名。
// 顺带钉住删除的护栏：分类下还有 server 时是 409 category_in_use，
// 不是一个"删成功了、server 悄悄变成未分类"的 204。
func TestCategoryChangeKeepsExposedToolNameStable(t *testing.T) {
	ds := startDownstream(t, "ping")
	e := newEnv(t, httpd.Options{})

	var cat struct {
		ID   string `json:"id"`
		Slug string `json:"slug"`
	}
	e.mustJSON(http.MethodPost, "/api/categories", map[string]any{
		"name": "天气", "slug": "weather",
	}, http.StatusCreated, &cat)

	var sv struct {
		ID string `json:"id"`
	}
	e.mustJSON(http.MethodPost, "/api/servers?probe=1", map[string]any{
		"key": "wx", "transport": "streamable_http", "url": ds, "categoryId": cat.ID,
	}, http.StatusCreated, &sv)
	e.enableAllTools()

	var tools toolListResp
	e.get("/api/tools?serverKey=wx", &tools)
	if tools.Total != 1 || tools.Items[0].ExposedName != "wx__ping" {
		t.Fatalf("聚合工具 = %+v, 期望 wx__ping", tools.Items)
	}

	// 改英文名 + 中文名 + 描述 → 对外工具名一个字符都不该变。
	e.mustJSON(http.MethodPut, "/api/categories/"+cat.ID, map[string]any{
		"name": "气象", "slug": "forecast", "description": "天气预报与实况",
	}, http.StatusOK, nil)

	e.get("/api/tools?serverKey=wx", &tools)
	if tools.Total != 1 || tools.Items[0].ExposedName != "wx__ping" {
		t.Fatalf("改分类后对外工具名不应变化: %+v", tools.Items)
	}
	// 分类变了要立刻反映在展示字段上（检索索引同步重建）。
	if tools.Items[0].Category != "气象" {
		t.Fatalf("分类展示名未更新: %+v", tools.Items[0])
	}

	// 分类下还挂着 server → 不能删（409），且 server 的归属不被清空。
	// 静默把 server 摘成"未分类"是一次用户没点过的批量改动，
	// 界面上只会表现为分类列空掉，回不到这次点击。
	resp, raw := e.call(http.MethodDelete, "/api/categories/"+cat.ID, nil)
	if resp.StatusCode != http.StatusConflict || !strings.Contains(string(raw), "category_in_use") {
		t.Fatalf("非空分类应 409 category_in_use，实际 %d\n响应: %s", resp.StatusCode, raw)
	}
	e.get("/api/tools?serverKey=wx", &tools)
	if tools.Items[0].Category != "气象" {
		t.Fatalf("删除被拒后 server 的归属不该变: %+v", tools.Items[0])
	}

	// 删掉 server 之后分类才能删：先摘空引用，再删分类。
	e.mustJSON(http.MethodDelete, "/api/servers/"+sv.ID, nil, http.StatusNoContent, nil)
	e.mustJSON(http.MethodDelete, "/api/categories/"+cat.ID, nil, http.StatusNoContent, nil)
	e.mustJSON(http.MethodDelete, "/api/categories/"+cat.ID, nil, http.StatusNotFound, nil)
}

/* ────────────────────────────── 单工具启停与变更记录 ────────────────────────────── */

// 默认禁用 → 详情/计数体现 → 启用后暴露 → 变更记录可查 → 禁用撤下；
// 未知工具返回 404。
func TestToolEnableDisableAPI(t *testing.T) {
	e := newEnv(t, httpd.Options{})
	ds := startDownstream(t, "ping")
	e.mustJSON(http.MethodPost, "/api/servers?probe=1", map[string]any{
		"key": "tk", "transport": "streamable_http", "url": ds,
		"categoryId": e.mustCategory(),
	}, http.StatusCreated, nil)

	var list serverListResp
	e.get("/api/servers", &list)
	id := list.Items[0].ID

	type item struct {
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
		Exposed bool   `json:"exposed"`
	}
	var detail struct {
		Enabled          bool   `json:"enabled"`
		EnabledToolCount int    `json:"enabledToolCount"`
		ExposedToolCount int    `json:"exposedToolCount"`
		ToolItems        []item `json:"toolItems"`
	}

	// 新工具默认禁用：有清单、没暴露。
	e.get("/api/servers/"+id, &detail)
	if len(detail.ToolItems) != 1 || detail.ToolItems[0].Name != "ping" || detail.ToolItems[0].Enabled {
		t.Fatalf("默认状态异常: %+v", detail.ToolItems)
	}
	if detail.EnabledToolCount != 0 || detail.ExposedToolCount != 0 {
		t.Errorf("默认计数应为 0/0，实际 %d/%d", detail.EnabledToolCount, detail.ExposedToolCount)
	}

	// 未启用的工具必须留在管理列表里并且标成"待启用"——
	// 否则"新工具默认禁用"就变成了"新工具默认消失"，界面上根本没有地方
	// 把它放出来。同时概览的三个口径要自洽：声明 1、暴露 0、未暴露 1。
	var tools toolListResp
	e.get("/api/tools?serverKey=tk", &tools)
	if tools.Total != 1 || tools.Items[0].Enabled || !tools.Items[0].Pending {
		t.Fatalf("未处理的新工具应留在列表里并标为待启用: %+v", tools.Items)
	}
	if n := e.aggToolCount(t); n != 0 {
		t.Errorf("待启用工具不该出现在 AI 的列表里，实际 %d", n)
	}
	var ov struct {
		Tools struct {
			Total         int   `json:"total"`
			Cached        int   `json:"cached"`
			Hidden        int   `json:"hidden"`
			PendingEnable int64 `json:"pendingEnable"`
		} `json:"tools"`
	}
	e.get("/api/overview", &ov)
	if ov.Tools.Total != 0 || ov.Tools.Cached != 1 || ov.Tools.Hidden != 1 || ov.Tools.PendingEnable != 1 {
		t.Errorf("概览工具口径 = %+v, 期望 total=0 cached=1 hidden=1 pending=1", ov.Tools)
	}

	// 启用：响应直接回整份详情，计数与暴露态同步翻转。
	e.mustJSON(http.MethodPost, "/api/servers/"+id+"/tools/ping/enable", nil, http.StatusOK, &detail)
	if len(detail.ToolItems) != 1 || !detail.ToolItems[0].Enabled || !detail.ToolItems[0].Exposed {
		t.Fatalf("启用后详情异常: %+v", detail.ToolItems)
	}
	if detail.EnabledToolCount != 1 || detail.ExposedToolCount != 1 {
		t.Errorf("启用后计数应为 1/1，实际 %d/%d", detail.EnabledToolCount, detail.ExposedToolCount)
	}
	// 启用后概览跟着变：全部暴露，没有欠处理的。
	e.get("/api/overview", &ov)
	if ov.Tools.Total != 1 || ov.Tools.Cached != 1 || ov.Tools.Hidden != 0 || ov.Tools.PendingEnable != 0 {
		t.Errorf("启用后概览工具口径 = %+v, 期望 total=1 cached=1 hidden=0 pending=0", ov.Tools)
	}

	// 聚合端点立即可见。
	e.get("/api/tools?serverKey=tk", &tools)
	if tools.Total != 1 || tools.Items[0].ExposedName != "tk__ping" || !tools.Items[0].Enabled {
		t.Fatalf("启用后聚合列表异常: %+v", tools.Items)
	}
	if tools.Items[0].Pending {
		t.Errorf("人工启用过的工具不该再算待处理: %+v", tools.Items[0])
	}

	// 变更记录：added（探活）+ enabled（人工），可按 server 过滤。
	var changes struct {
		Total int `json:"total"`
		Items []struct {
			ToolName string `json:"toolName"`
			Action   string `json:"action"`
			Actor    string `json:"actor"`
		} `json:"items"`
	}
	e.get("/api/tool-changes?serverId="+id, &changes)
	if changes.Total != 2 {
		t.Fatalf("变更记录数 = %d, want 2", changes.Total)
	}
	if changes.Items[0].Action != "enabled" || changes.Items[0].Actor != "admin" {
		t.Errorf("最新一条应为 admin 的 enabled: %+v", changes.Items[0])
	}

	// 未知工具：404，而不是静默成功。
	resp, _ := e.call(http.MethodPost, "/api/servers/"+id+"/tools/ghost/enable", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("未知工具 = %d, want 404", resp.StatusCode)
	}

	// 禁用：从 AI 的列表里撤下，但管理列表里留着——开关要能再拨回来。
	e.mustJSON(http.MethodPost, "/api/servers/"+id+"/tools/ping/disable", nil, http.StatusOK, &detail)
	if detail.ToolItems[0].Enabled || detail.ExposedToolCount != 0 {
		t.Fatalf("禁用后详情异常: %+v", detail)
	}
	// 唯一的启用工具被禁用后，server 应自动停用（无可用工具的 server
	// 不该留在启用状态空转：探活照跑、健康统计照算，AI 却看不到任何工具）。
	if detail.Enabled {
		t.Errorf("禁用唯一启用工具后 server 仍为启用状态, 期望自动停用")
	}
	e.get("/api/tools?serverKey=tk", &tools)
	// server 已自动停用：工具随 server 从聚合浏览器退场（与手动停用 server
	// 的既有行为一致），但工具行仍在库里，server 详情页照常可见、可再启用。
	if tools.Total != 0 {
		t.Errorf("server 停用后聚合浏览器不应再列出其工具: %+v", tools.Items)
	}
	e.get("/api/servers/"+id, &detail)
	if len(detail.ToolItems) != 1 || detail.ToolItems[0].Enabled {
		t.Errorf("停用后工具行应保留在详情里且开关为关: %+v", detail.ToolItems)
	}
	if n := e.aggToolCount(t); n != 0 {
		t.Errorf("禁用后 AI 可见工具数 = %d, 期望 0", n)
	}
}
