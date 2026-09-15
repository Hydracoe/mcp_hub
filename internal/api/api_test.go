package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hydra/mcp-hub/internal/httpd"
	"github.com/hydra/mcp-hub/internal/hub"
	"github.com/hydra/mcp-hub/internal/store"
	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"go.uber.org/zap"
)

// --- 测试环境 ---

// env 是一套跑起来的网关：真实 SQLite + 真实 MCP 下游 + 真实 HTTP 层。
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

	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	svc := hub.NewService(st, nil)
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
	ID               string   `json:"id"`
	Key              string   `json:"key"`
	Name             string   `json:"name"`
	Transport        string   `json:"transport"`
	Target           string   `json:"target"`
	Enabled          bool     `json:"enabled"`
	Status           string   `json:"status"`
	ProtocolEra      string   `json:"protocolEra"`
	ProtocolVersion  string   `json:"protocolVersion"`
	ToolCount        int      `json:"toolCount"`
	ExposedToolCount int      `json:"exposedToolCount"`
	ToolFilterMode   string   `json:"toolFilterMode"`
	ToolFilterList   []string `json:"toolFilterList"`
	LastErrorKind    string   `json:"lastErrorKind"`
	LastErrorHint    string   `json:"lastErrorHint"`
	LastLatencyMS    int64    `json:"lastLatencyMs"`

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
		Available   bool            `json:"available"`
		InputSchema json.RawMessage `json:"inputSchema"`
	} `json:"items"`
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
		Total   int `json:"total"`
		Enabled int `json:"enabled"`
		Healthy int `json:"healthy"`
		Offline int `json:"offline"`
		Unknown int `json:"unknown"`
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
		"key":       "ds",
		"transport": "streamable_http",
		"url":       ds,
		"timeoutMs": 5000,
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
	if created.ToolCount != 2 || created.ExposedToolCount != 2 {
		t.Errorf("工具计数 = %d/%d, 期望 2/2", created.ToolCount, created.ExposedToolCount)
	}

	// 列表页要能看到它，且列出的是下游原始工具名。
	var list serverListResp
	e.get("/api/servers", &list)
	if list.Total != 1 || list.Items[0].Key != "ds" {
		t.Fatalf("列表 = %+v", list)
	}

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

	// 停用：工具应从聚合列表消失。
	var disabled serverResp
	e.mustJSON(http.MethodPost, "/api/servers/"+created.ID+"/disable", nil, http.StatusOK, &disabled)
	if disabled.Enabled {
		t.Error("停用后 enabled 仍为 true")
	}
	e.get("/api/tools", &tools)
	if tools.Total != 0 {
		t.Errorf("停用后聚合工具数 = %d, 期望 0", tools.Total)
	}

	// 启用：恢复。
	var enabled serverResp
	e.mustJSON(http.MethodPost, "/api/servers/"+created.ID+"/enable", nil, http.StatusOK, &enabled)
	if !enabled.Enabled {
		t.Error("启用后 enabled 为 false")
	}
	e.get("/api/tools", &tools)
	if tools.Total != 2 {
		t.Errorf("重新启用后工具数 = %d, 期望 2", tools.Total)
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

// 管理接口说"已聚合"，AI 客户端就必须看得到——否则界面只是个装饰。
func TestMCPEndpointReflectsAdminChanges(t *testing.T) {
	e := newEnv(t, httpd.Options{})
	ds := startDownstream(t, "ping")

	e.mustJSON(http.MethodPost, "/api/servers?probe=1", map[string]any{
		"key": "live", "transport": "streamable_http", "url": ds,
	}, http.StatusCreated, nil)

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
		"key":       "sec",
		"transport": "streamable_http",
		"url":       ds,
		"headers":   map[string]string{"Authorization": token},
		"env":       []string{"API_KEY=env-secret", "PLAIN=visible"},
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

	saved, err := e.store.GetServer(context.Background(), id)
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

	saved, _ = e.store.GetServer(context.Background(), id)
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

func TestDuplicateKeyIsConflict(t *testing.T) {
	e := newEnv(t, httpd.Options{})
	ds := startDownstream(t, "x")

	body := map[string]any{"key": "dup", "transport": "streamable_http", "url": ds}
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

// --- 批量导入 ---

func TestImportMCPServersFormat(t *testing.T) {
	e := newEnv(t, httpd.Options{})
	ds := startDownstream(t, "t1", "t2")

	body := map[string]any{
		"mcpServers": map[string]any{
			"remote": map[string]any{
				"url":     ds,
				"headers": map[string]string{"Authorization": "Bearer t"},
			},
			"my_local": map[string]any{
				"command": "definitely-not-a-real-binary",
				"args":    []string{"--flag"},
				"env":     map[string]string{"TOKEN": "abc"},
				"enabled": false,
			},
		},
	}

	var out struct {
		Total    int `json:"total"`
		Imported int `json:"imported"`
		Failed   int `json:"failed"`
		Results  []struct {
			Key         string `json:"key"`
			RenamedFrom string `json:"renamedFrom"`
			Status      string `json:"status"`
			Error       string `json:"error"`
		} `json:"results"`
	}
	e.mustJSON(http.MethodPost, "/api/servers/import?probe=1", body, http.StatusOK, &out)

	if out.Total != 2 || out.Imported != 2 || out.Failed != 0 {
		t.Fatalf("导入结果 = %+v", out)
	}

	byKey := map[string]string{}
	for _, r := range out.Results {
		byKey[r.Key] = r.Status
	}
	if byKey["remote"] != "created" {
		t.Errorf("remote 状态 = %q, 期望 created", byKey["remote"])
	}
	// 下划线不是合法前缀字符，要被折成连字符并标注原键名——
	// 静默改名会让工具前缀和用户的预期对不上。
	if _, ok := byKey["my-local"]; !ok {
		t.Fatalf("带下划线的 key 未被折成合法前缀: %+v", out.Results)
	}

	var list serverListResp
	e.get("/api/servers", &list)
	if list.Total != 2 {
		t.Fatalf("导入后 server 数 = %d, 期望 2", list.Total)
	}
	for _, item := range list.Items {
		switch item.Key {
		case "remote":
			if item.Status != "healthy" || item.ToolCount != 2 {
				t.Errorf("remote 导入后状态异常: %+v", item)
			}
			if !strings.Contains(item.Target, ds) {
				t.Errorf("remote 的 target = %q", item.Target)
			}
		case "my-local":
			if item.Enabled {
				t.Error("enabled=false 的项不应被启用")
			}
			if item.Transport != "stdio" {
				t.Errorf("带 command 的项应被识别为 stdio，得到 %q", item.Transport)
			}
		}
	}

	// 再导一次：默认跳过已存在的项，不误覆盖。
	var again struct {
		Skipped int `json:"skipped"`
	}
	e.mustJSON(http.MethodPost, "/api/servers/import", body, http.StatusOK, &again)
	if again.Skipped != 2 {
		t.Errorf("重复导入 skipped = %d, 期望 2", again.Skipped)
	}
}

// --- 工具过滤 ---

func TestToolFilterThroughAPI(t *testing.T) {
	e := newEnv(t, httpd.Options{})
	ds := startDownstream(t, "keep", "drop")

	e.mustJSON(http.MethodPost, "/api/servers?probe=1", map[string]any{
		"key": "f", "transport": "streamable_http", "url": ds,
	}, http.StatusCreated, nil)

	var list serverListResp
	e.get("/api/servers", &list)
	id := list.Items[0].ID

	// 配黑名单。
	var updated serverResp
	e.mustJSON(http.MethodPut, "/api/servers/"+id, map[string]any{
		"key": "f", "transport": "streamable_http", "url": ds,
		"toolFilterMode": "block",
		"toolFilterList": []string{"drop"},
	}, http.StatusOK, &updated)

	if updated.ToolFilterMode != "block" {
		t.Errorf("过滤模式 = %q", updated.ToolFilterMode)
	}
	// 缓存里仍有 2 个，但只有 1 个对外暴露——这两个数字的差异
	// 正是"过滤正在生效"的证据。
	if updated.ToolCount != 2 || updated.ExposedToolCount != 1 {
		t.Errorf("工具计数 = %d/%d, 期望 2/1", updated.ToolCount, updated.ExposedToolCount)
	}

	var tools toolListResp
	e.get("/api/tools?serverKey=f", &tools)
	if tools.Total != 1 || tools.Items[0].ExposedName != "f__keep" {
		t.Fatalf("过滤后聚合列表 = %+v", tools.Items)
	}
}

// --- 故障下游 ---

// 一个连不上的下游，不该让管理接口或聚合端点整体失效。
func TestDeadDownstreamIsReportedNotFatal(t *testing.T) {
	e := newEnv(t, httpd.Options{})
	good := startDownstream(t, "fine")

	e.mustJSON(http.MethodPost, "/api/servers?probe=1", map[string]any{
		"key": "good", "transport": "streamable_http", "url": good, "timeoutMs": 3000,
	}, http.StatusCreated, nil)

	var bad serverResp
	e.mustJSON(http.MethodPost, "/api/servers?probe=1", map[string]any{
		"key": "bad", "transport": "streamable_http", "url": deadURL(t), "timeoutMs": 3000,
	}, http.StatusCreated, &bad)

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
	}, http.StatusCreated, nil)

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
	}, http.StatusCreated, nil)

	var out struct {
		MCP        string            `json:"mcpEndpoint"`
		SSE        string            `json:"sseEndpoint"`
		Health     string            `json:"healthEndpoint"`
		MCPClients map[string]string `json:"mcpServers"`
		ToolCount  int               `json:"toolCount"`
		Summary    string            `json:"summary"`
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
}
