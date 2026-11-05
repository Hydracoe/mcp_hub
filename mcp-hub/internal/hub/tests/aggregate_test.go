package tests

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hydra/mcp-hub/internal/cache/cachetest"
	"github.com/hydra/mcp-hub/internal/downstream"
	"github.com/hydra/mcp-hub/internal/httpd"
	"github.com/hydra/mcp-hub/internal/hub"
	"github.com/hydra/mcp-hub/internal/store"
	"github.com/hydra/mcp-hub/internal/store/logs"
	"github.com/hydra/mcp-hub/internal/store/servers"
	"github.com/hydra/mcp-hub/internal/store/storetest"
	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

// toolSpec 描述一个要挂到假下游上的工具。
type toolSpec struct {
	name    string
	desc    string
	handler mcpserver.ToolHandlerFunc
}

// echoTool 回显收到的 arguments 原文。
// 用 raw 字节而不是解析后的 map：要证明网关没有在中间对参数做任何有损转换。
func echoTool(name string) toolSpec {
	return toolSpec{
		name: name,
		desc: "echo back the raw arguments",
		handler: func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			raw := req.Params.RawArguments
			if len(raw) == 0 {
				raw = json.RawMessage(`{}`)
			}
			return mcp.NewToolResultText(string(raw)), nil
		},
	}
}

// taggedTool 返回一个带固定标记的工具，用来验证路由没串线。
func taggedTool(name, tag string) toolSpec {
	return toolSpec{
		name: name,
		desc: "returns a fixed tag",
		handler: func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return mcp.NewToolResultText(tag), nil
		},
	}
}

// newMCPHandler 组一个真实的 MCP server handler。
// 单独抽出来是为了让测试能在外面再包一层（比如校验凭据的中间件），
// 从而观察到"请求究竟带了什么"——那比断言网关内部状态可靠得多。
func newMCPHandler(tools []toolSpec) http.Handler {
	srv := mcpserver.NewMCPServer("fake-downstream", "1.0.0",
		mcpserver.WithToolCapabilities(false))
	for _, ts := range tools {
		if ts.handler == nil {
			ts.handler = func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return mcp.NewToolResultText("ok"), nil
			}
		}
		opts := []mcp.ToolOption{}
		if ts.desc != "" {
			opts = append(opts, mcp.WithDescription(ts.desc))
		}
		srv.AddTool(mcp.NewTool(ts.name, opts...), ts.handler)
	}
	return mcpserver.NewStreamableHTTPServer(srv)
}

// startDownstream 起一个真实的 MCP server，返回它的端点 URL。
//
// 不 mock 协议：httptest + mcp-go 内建 server 就能跑出完整的 Streamable HTTP 往返。
func startDownstream(t *testing.T, tools ...toolSpec) string {
	t.Helper()

	httpSrv := httptest.NewServer(newMCPHandler(tools))
	t.Cleanup(httpSrv.Close)
	return httpSrv.URL
}

// deadURL 返回一个确定连不上的地址：起一个 server 再立刻关掉，端口随即被回收，
// 比手写固定端口可靠（那个端口随时可能被别的进程占着）。
func deadURL(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()
	return url
}

// env 是一套跑起来的测试环境。
type env struct {
	svc    *hub.Service
	store  *store.Store
	ts     *httptest.Server
	client *client.Client
}

// newEnv 起一个完整的网关：PostgreSQL + 服务 + HTTP 层 + 真实 MCP 客户端。
func newEnv(t *testing.T, specs []downstream.Spec) *env {
	t.Helper()

	st, err := store.Open(storetest.DSN(t))
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	ctx := context.Background()
	for _, spec := range specs {
		if err := st.Servers.Upsert(ctx, servers.Config{Spec: spec, Enabled: true}); err != nil {
			t.Fatalf("写入 server %s 失败: %v", spec.Key, err)
		}
	}

	svc := hub.NewServiceWithCache(st, nil, cachetest.New())
	t.Cleanup(func() { svc.Close() })

	if len(specs) > 0 {
		svc.RefreshAll(ctx)
		enableAllCachedTools(t, st, svc)
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

// enableAllCachedTools 把缓存里的工具全部显式启用并重建聚合快照。
// 生产语义是"探活新发现的工具默认禁用，由管理员逐个开启"；大多数用例验证的是
// 开启之后的链路，这里统一模拟"管理员已开启"的存量环境（默认禁用另有测试覆盖）。
func enableAllCachedTools(t *testing.T, st *store.Store, svc *hub.Service) {
	t.Helper()
	ctx := context.Background()
	tools, err := st.Servers.ListTools(ctx)
	if err != nil {
		t.Fatalf("读取工具缓存失败: %v", err)
	}
	for _, tl := range tools {
		if tl.Enabled {
			continue
		}
		if err := svc.SetToolEnabled(ctx, tl.ServerID, tl.Name, true); err != nil {
			t.Fatalf("启用工具 %s/%s 失败: %v", tl.ServerID, tl.Name, err)
		}
	}
}

// remoteSpec 造一个指向远程端点的配置。
func remoteSpec(key, url string) downstream.Spec {
	return downstream.Spec{
		Key:       key,
		Name:      key,
		Transport: downstream.TransportStreamableHTTP,
		URL:       url,
		Timeout:   10 * time.Second,
	}
}

// stdioSpec 造一个拉起本地子进程的配置。
func stdioSpec(key, bin string) downstream.Spec {
	return downstream.Spec{
		Key:       key,
		Name:      key,
		Transport: downstream.TransportStdio,
		Command:   bin,
		Timeout:   10 * time.Second,
	}
}

// listTools 通过聚合端点拉取工具列表。
func (e *env) listTools(t *testing.T) []mcp.Tool {
	t.Helper()
	res, err := e.client.ListTools(context.Background(), mcp.ListToolsRequest{})
	if err != nil {
		t.Fatalf("tools/list 失败: %v", err)
	}
	return res.Tools
}

// callTool 通过聚合端点调用一个工具。
func (e *env) callTool(t *testing.T, name string, args string) *mcp.CallToolResult {
	t.Helper()

	req := mcp.CallToolRequest{}
	req.Params.Name = name
	if args != "" {
		req.Params.RawArguments = json.RawMessage(args)
	}

	res, err := e.client.CallTool(context.Background(), req)
	if err != nil {
		t.Fatalf("tools/call %s 协议层失败: %v", name, err)
	}
	return res
}

// textOf 把结果的第一个文本块取出来。
func textOf(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if len(res.Content) == 0 {
		t.Fatalf("结果没有 content")
	}
	text, ok := mcp.AsTextContent(res.Content[0])
	if !ok {
		t.Fatalf("第一个 content 不是文本: %T", res.Content[0])
	}
	return text.Text
}

func TestAggregationExposesPrefixedTools(t *testing.T) {
	alphaURL := startDownstream(t, echoTool("echo"), taggedTool("ping", "pong"))
	betaURL := startDownstream(t, taggedTool("hello", "world"))

	e := newEnv(t, []downstream.Spec{
		remoteSpec("alpha", alphaURL),
		remoteSpec("beta", betaURL),
	})

	tools := e.listTools(t)
	got := make(map[string]bool, len(tools))
	for _, tool := range tools {
		got[tool.Name] = true
	}

	for _, want := range []string{"alpha__echo", "alpha__ping", "beta__hello"} {
		if !got[want] {
			t.Errorf("聚合列表缺少 %q，实际: %v", want, keys(got))
		}
	}
	if len(tools) != 3 {
		t.Errorf("工具数 = %d, 期望 3", len(tools))
	}

	// 下游原始名不应出现在列表里——AI 只能按对外名调用。
	if got["echo"] {
		t.Error("列表里出现了未经前缀的原始工具名 echo")
	}

	// Title 保留原始名，便于界面展示与人工排查。
	for _, tool := range tools {
		if tool.Name == "alpha__echo" && tool.Title != "echo" {
			t.Errorf("alpha__echo 的 Title = %q, 期望 echo", tool.Title)
		}
	}
}

func TestToolSchemaIsPassedThrough(t *testing.T) {
	srv := mcpserver.NewMCPServer("schema-downstream", "1.0.0", mcpserver.WithToolCapabilities(false))
	srv.AddTool(
		mcp.NewTool("needs_args",
			mcp.WithDescription("needs a city"),
			mcp.WithString("city", mcp.Required(), mcp.Description("城市名")),
			mcp.WithNumber("limit", mcp.Description("上限")),
		),
		func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return mcp.NewToolResultText("ok"), nil
		},
	)
	httpSrv := httptest.NewServer(mcpserver.NewStreamableHTTPServer(srv))
	t.Cleanup(httpSrv.Close)

	e := newEnv(t, []downstream.Spec{remoteSpec("geo", httpSrv.URL)})

	for _, tool := range e.listTools(t) {
		if tool.Name != "geo__needs_args" {
			continue
		}
		if tool.Description != "needs a city" {
			t.Errorf("description 被改写了: %q", tool.Description)
		}

		// 入参定义必须完整抵达客户端，否则 AI 会照着一个空 schema 乱填参数。
		if tool.InputSchema.Type != "object" {
			t.Errorf("schema type = %q, 期望 object", tool.InputSchema.Type)
		}
		if len(tool.InputSchema.Required) != 1 || tool.InputSchema.Required[0] != "city" {
			t.Errorf("required = %v, 期望 [city]", tool.InputSchema.Required)
		}
		for _, want := range []string{"city", "limit"} {
			if _, ok := tool.InputSchema.Properties[want]; !ok {
				t.Errorf("schema 丢失属性 %q", want)
			}
		}
		return
	}
	t.Fatal("没找到 geo__needs_args")
}

func TestCallRoutesToCorrectDownstream(t *testing.T) {
	alphaURL := startDownstream(t, taggedTool("search", "from-alpha"))
	betaURL := startDownstream(t, taggedTool("search", "from-beta"))

	// 两个下游提供同名工具。这正是前缀要解决的问题：
	// 没有它，AI 无法表达"我要调 alpha 的 search"。
	e := newEnv(t, []downstream.Spec{
		remoteSpec("alpha", alphaURL),
		remoteSpec("beta", betaURL),
	})

	if got := textOf(t, e.callTool(t, "alpha__search", `{}`)); got != "from-alpha" {
		t.Errorf("alpha__search 返回 %q, 期望 from-alpha", got)
	}
	if got := textOf(t, e.callTool(t, "beta__search", `{}`)); got != "from-beta" {
		t.Errorf("beta__search 返回 %q, 期望 from-beta", got)
	}
}

func TestRoutingDoesNotSplitExposedName(t *testing.T) {
	// 下游工具名自带分隔符。若路由靠切分反推，这里必然串线。
	alphaURL := startDownstream(t, taggedTool("a__b", "alpha-double"))
	betaURL := startDownstream(t, taggedTool("b", "beta-single"))

	e := newEnv(t, []downstream.Spec{
		remoteSpec("alpha", alphaURL),
		remoteSpec("beta", betaURL),
	})

	if got := textOf(t, e.callTool(t, "alpha__a__b", `{}`)); got != "alpha-double" {
		t.Errorf("alpha__a__b 返回 %q, 期望 alpha-double", got)
	}
	if got := textOf(t, e.callTool(t, "beta__b", `{}`)); got != "beta-single" {
		t.Errorf("beta__b 返回 %q, 期望 beta-single", got)
	}
}

func TestArgumentsPassThroughUnchanged(t *testing.T) {
	dsURL := startDownstream(t, echoTool("echo"))
	e := newEnv(t, []downstream.Spec{remoteSpec("x", dsURL)})

	// 9007199254740993 超过 2^53，走 float64 中转必然失真。
	args := `{"big":9007199254740993,"nested":{"list":[1,2,3]},"s":"中文","f":0.1}`
	got := textOf(t, e.callTool(t, "x__echo", args))

	var want, have map[string]any
	if err := json.Unmarshal([]byte(args), &want); err != nil {
		t.Fatalf("测试参数本身不合法: %v", err)
	}
	if err := json.Unmarshal([]byte(got), &have); err != nil {
		t.Fatalf("回显内容不是合法 JSON: %q", got)
	}

	if !strings.Contains(got, "9007199254740993") {
		t.Errorf("大整数在网关这一跳失真了: %s", got)
	}
	if have["s"] != want["s"] {
		t.Errorf("字符串字段变了: %v != %v", have["s"], want["s"])
	}
	if have["f"] != want["f"] {
		t.Errorf("浮点字段变了: %v != %v", have["f"], want["f"])
	}
}

func TestEmptyArgumentsBecomeEmptyObject(t *testing.T) {
	// 下游声明了对象入参时，null 会被直接判为参数类型错误。
	dsURL := startDownstream(t, echoTool("echo"))
	e := newEnv(t, []downstream.Spec{remoteSpec("x", dsURL)})

	if got := textOf(t, e.callTool(t, "x__echo", "")); got != "{}" {
		t.Errorf("无参调用传给下游的是 %q, 期望 {}", got)
	}
}

func TestResultPassThrough(t *testing.T) {
	srv := mcpserver.NewMCPServer("rich-downstream", "1.0.0", mcpserver.WithToolCapabilities(false))
	srv.AddTool(mcp.NewTool("rich"), func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		res := mcp.NewToolResultText("第一块")
		res.Content = append(res.Content, mcp.NewImageContent("aGVsbG8=", "image/png"))
		res.StructuredContent = map[string]any{"count": 3}
		return res, nil
	})
	srv.AddTool(mcp.NewTool("failing"), func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		// 下游自己判定这次执行失败，但协议层是成功的。
		return mcp.NewToolResultError("下游业务错误：余额不足"), nil
	})
	httpSrv := httptest.NewServer(mcpserver.NewStreamableHTTPServer(srv))
	t.Cleanup(httpSrv.Close)

	e := newEnv(t, []downstream.Spec{remoteSpec("rich", httpSrv.URL)})

	res := e.callTool(t, "rich__rich", `{}`)
	if len(res.Content) != 2 {
		t.Errorf("content 块数 = %d, 期望 2（文本 + 图片）", len(res.Content))
	}
	if res.StructuredContent == nil {
		t.Error("structuredContent 被丢掉了")
	}
	if res.IsError {
		t.Error("成功结果被标成了错误")
	}

	// 下游标了 isError 的工具失败，对外仍应是 isError：
	// 网关不替下游改写语义。
	failed := e.callTool(t, "rich__failing", `{}`)
	if !failed.IsError {
		t.Error("下游的 isError 结果被网关抹平成了成功")
	}
	if !strings.Contains(textOf(t, failed), "余额不足") {
		t.Errorf("下游错误信息没有透传: %s", textOf(t, failed))
	}
}

func TestDownstreamFailureIsIsolated(t *testing.T) {
	goodURL := startDownstream(t, taggedTool("ok", "alive"))

	// 这个下游先活着（让探活把工具拉进聚合列表），再被切成 502。
	// 这是真实世界里最常见的样子：下游不是从没通过，而是跑着跑着挂了。
	var dying atomic.Bool
	brkSrv := mcpserver.NewMCPServer("brk", "1.0.0", mcpserver.WithToolCapabilities(false))
	brkSrv.AddTool(mcp.NewTool("thing"), func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText("should not be reached once broken"), nil
	})
	brkHandler := mcpserver.NewStreamableHTTPServer(brkSrv)
	brk := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if dying.Load() {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		brkHandler.ServeHTTP(w, r)
	}))
	t.Cleanup(brk.Close)

	e := newEnv(t, []downstream.Spec{
		remoteSpec("good", goodURL),
		remoteSpec("broken", brk.URL),
	})

	// 前提：故障下游的工具此刻确实在聚合列表里。
	if n := e.svc.Registry().Current().ToolCount(); n != 2 {
		t.Fatalf("聚合应有 2 个工具，实际 %d", n)
	}

	dying.Store(true)

	// 坏掉的 server 不该让聚合端点整体失败：列表仍然完整可读。
	tools := e.listTools(t)
	if len(tools) != 2 {
		t.Errorf("下游故障后工具列表 = %d 项，期望 2 项（缓存应保留）", len(tools))
	}

	// 健康的下游照常工作。
	if got := textOf(t, e.callTool(t, "good__ok", `{}`)); got != "alive" {
		t.Errorf("健康下游受影响，返回 %q", got)
	}

	// 故障下游的调用返回 isError 而不是协议错误——
	// 客户端会话不该因为某个下游挂了而判定异常。
	res := e.callTool(t, "broken__thing", `{}`)
	if !res.IsError {
		t.Fatal("故障下游的调用没有返回 isError")
	}
	msg := textOf(t, res)
	if !strings.Contains(msg, "broken") {
		t.Errorf("错误信息没指明是哪个上游: %s", msg)
	}
	if !strings.Contains(msg, "502") && !strings.Contains(msg, "异常") {
		t.Errorf("错误信息没给出可定位的原因: %s", msg)
	}
}

func TestUnknownToolIsRejectedClearly(t *testing.T) {
	dsURL := startDownstream(t, taggedTool("real", "ok"))
	e := newEnv(t, []downstream.Spec{remoteSpec("x", dsURL)})

	// 调用一个不存在的工具。MCP 服务端会在协议层直接拒绝，
	// 不会走到聚合 handler——这正确：无效调用不该被当成一次转发失败。
	req := mcp.CallToolRequest{}
	req.Params.Name = "x__nonexistent"
	_, err := e.client.CallTool(context.Background(), req)
	if err == nil {
		t.Fatal("调用不存在的工具应该报错")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("错误信息不够明确: %v", err)
	}
}

func TestProbeFailureKeepsCachedTools(t *testing.T) {
	dsURL := startDownstream(t, taggedTool("persist", "v1"))

	spec := remoteSpec("flaky", dsURL)
	e := newEnv(t, []downstream.Spec{spec})

	if n := e.svc.Registry().Current().ToolCount(); n != 1 {
		t.Fatalf("首次聚合应有 1 个工具，实际 %d", n)
	}

	// 把 URL 换成死地址再探活：模拟下游抖动。
	ctx := context.Background()
	srv, err := e.store.Servers.GetByKey(ctx, "flaky")
	if err != nil {
		t.Fatalf("读取 server 失败: %v", err)
	}
	broken := srv.Spec
	broken.URL = deadURL(t)
	if err := e.store.Servers.Upsert(ctx, servers.Config{Spec: broken, Enabled: true}); err != nil {
		t.Fatalf("更新配置失败: %v", err)
	}

	report := e.svc.RefreshAll(ctx)
	if report.Failed != 1 {
		t.Errorf("应有 1 个失败，实际 %d", report.Failed)
	}

	// 探活失败但工具缓存必须留住——否则 AI 手上的工具会凭空消失。
	if n := e.svc.Registry().Current().ToolCount(); n != 1 {
		t.Errorf("探活失败后工具数 = %d，期望保留 1 个缓存工具", n)
	}
}

func TestDisabledServerIsExcluded(t *testing.T) {
	onURL := startDownstream(t, taggedTool("on_tool", "on"))
	offURL := startDownstream(t, taggedTool("off_tool", "off"))

	e := newEnv(t, []downstream.Spec{
		remoteSpec("on", onURL),
		remoteSpec("off", offURL),
	})

	ctx := context.Background()
	srv, err := e.store.Servers.GetByKey(ctx, "off")
	if err != nil {
		t.Fatalf("读取 server 失败: %v", err)
	}
	if err := e.svc.SetEnabled(ctx, srv.Spec.ID, false); err != nil {
		t.Fatalf("停用失败: %v", err)
	}

	tools := e.listTools(t)
	for _, tool := range tools {
		if tool.Name == "off__off_tool" {
			t.Error("已停用的 server 的工具仍然出现在聚合列表里")
		}
	}
	if len(tools) != 1 {
		t.Errorf("工具数 = %d, 期望 1", len(tools))
	}
}

// 端到端钉住创建向导的勾选语义：创建时带的 EnabledTools 决定首次探活后
// 哪些工具直接进入聚合端点，其余保持禁用等人工开启；名单一次性消费。
func TestBootEnabledToolsEndToEnd(t *testing.T) {
	ds := startDownstream(t, taggedTool("keep", "k"), taggedTool("drop", "d"))
	e := newEnv(t, nil)
	ctx := context.Background()

	spec := remoteSpec("f", ds)
	if err := e.store.Servers.Upsert(ctx, servers.Config{
		Spec:         spec,
		Enabled:      true,
		EnabledTools: []string{"keep"},
	}); err != nil {
		t.Fatalf("Upsert 失败: %v", err)
	}
	e.svc.RefreshAll(ctx)

	if got := toolNames(e.listTools(t)); len(got) != 1 || got[0] != "f__keep" {
		t.Fatalf("引导名单后聚合端点 = %v, want [f__keep]", got)
	}

	// 名单外工具在缓存里禁用着，人工启用后立刻可见。
	srv, _ := e.store.Servers.GetByKey(ctx, "f")
	if err := e.svc.SetToolEnabled(ctx, srv.Spec.ID, "drop", true); err != nil {
		t.Fatalf("启用失败: %v", err)
	}
	if got := toolNames(e.listTools(t)); len(got) != 2 {
		t.Fatalf("全部启用后 = %v, want 2 个工具", got)
	}
}

func TestCallsAreLogged(t *testing.T) {
	dsURL := startDownstream(t, taggedTool("logged", "ok"))
	e := newEnv(t, []downstream.Spec{remoteSpec("lg", dsURL)})

	e.callTool(t, "lg__logged", `{}`)

	// 日志是异步落库的，等它冲一批。
	var entries []logs.LogEntry
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		entries, _, _ = e.store.Logs.List(context.Background(), logs.LogQuery{Limit: 10})
		if len(entries) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if len(entries) == 0 {
		t.Fatal("调用没有被记入日志")
	}
	entry := entries[0]
	if entry.ExposedName != "lg__logged" {
		t.Errorf("日志里的工具名 = %q", entry.ExposedName)
	}
	if entry.ToolName != "logged" {
		t.Errorf("日志应记录下游原始名，得到 %q", entry.ToolName)
	}
	if entry.ServerKey != "lg" {
		t.Errorf("日志里的 server key = %q", entry.ServerKey)
	}
	if entry.Status != "ok" {
		t.Errorf("日志状态 = %q, 期望 ok", entry.Status)
	}
}

func TestConcurrentCalls(t *testing.T) {
	// 一个慢工具 + 高并发，验连接能被复用而不是每条请求重建。
	dsURL := startDownstream(t, toolSpec{
		name: "slow",
		handler: func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			select {
			case <-time.After(20 * time.Millisecond):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			return mcp.NewToolResultText(req.Params.Name), nil
		},
	})
	e := newEnv(t, []downstream.Spec{remoteSpec("conc", dsURL)})

	const n = 20
	var wg sync.WaitGroup
	errs := make(chan error, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := mcp.CallToolRequest{}
			req.Params.Name = "conc__slow"
			res, err := e.client.CallTool(context.Background(), req)
			if err != nil {
				errs <- fmt.Errorf("调用失败: %w", err)
				return
			}
			if res.IsError {
				errs <- fmt.Errorf("返回 isError: %v", res.Content)
			}
		}()
	}

	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestStdioDownstreamSurvivesAcrossRequests(t *testing.T) {
	bin := buildEchoServer(t)
	e := newEnv(t, []downstream.Spec{stdioSpec("std", bin)})

	// 关键：stdio 传输底层是 exec.CommandContext，若把某个请求的 ctx 传给 Start，
	// 那个请求一结束子进程就被杀，其后每次调用都只能看到一个已死的连接。
	for i := 1; i <= 5; i++ {
		res := e.callTool(t, "std__echo", `{"n":1}`)
		if res.IsError {
			t.Fatalf("第 %d 次调用失败，stdio 连接没能跨请求存活: %s", i, textOf(t, res))
		}
	}
}

func TestStdioDownstreamIsolatesFailure(t *testing.T) {
	bin := buildEchoServer(t)

	e := newEnv(t, []downstream.Spec{
		stdioSpec("ghost", bin),
		stdioSpec("real", bin),
	})

	// 两个都先确认可用，这样后面坏掉的那个工具确实已在聚合列表里。
	if r := e.callTool(t, "ghost__echo", `{}`); r.IsError {
		t.Fatalf("前置条件不成立，ghost 一开始就不可用: %s", textOf(t, r))
	}
	ghostID := e.serverIDOf(t, "ghost")

	// 把 ghost 指向不存在的可执行文件，并让它立即生效。
	if _, err := e.svc.ApplyServer(context.Background(), servers.Config{
		Spec:    stdioSpec("ghost", "/nonexistent/binary-mcp-hub-test"),
		Enabled: true,
	}); err != nil {
		t.Fatalf("更新配置失败: %v", err)
	}

	// 目标已换，旧工具不应再对外暴露；探活要把新目标的失败分类带出来。
	probe, err := e.svc.RefreshTools(context.Background(), ghostID)
	if err != nil {
		t.Fatalf("刷新工具失败: %v", err)
	}
	if probe.OK || probe.Kind != downstream.KindSpawnFailed {
		t.Fatalf("坏目标探活应返回 SPAWN_FAILED，实际 ok=%v kind=%s", probe.OK, probe.Kind)
	}
	for _, entry := range e.svc.Registry().Current().ToolEntries() {
		if entry.Exposed == "ghost__echo" {
			t.Errorf("目标已坏，聚合快照仍暴露 %s", entry.Exposed)
		}
	}

	// 另一个 stdio 下游不受影响——这是进程级隔离，不是同进程内的异常吞掉。
	if r := e.callTool(t, "real__echo", `{}`); r.IsError {
		t.Errorf("健康 stdio 下游受影响: %s", textOf(t, r))
	}
}

// buildEchoServer 现场编译测试用的 stdio MCP server。
// 不把二进制提交进仓库：平台差异与陈旧版本会带来更难查的问题。
func buildEchoServer(t *testing.T) string {
	t.Helper()

	out := filepath.Join(t.TempDir(), "echoserver")
	cmd := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"),
		"build", "-o", out, "./testdata/echoserver")
	cmd.Dir = "../../downstream"

	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("编译测试用 stdio server 失败: %v\n%s", err, b)
	}
	return out
}

// meta-tool 的注册不能被"工具列表没变就不动 MCP server"的指纹短路吃掉。
// 复现启动顺序：Reload 先刷新（下游工具已写进指纹），AttachEmbedding 随后才注册
// meta-tool；指纹若只覆盖下游工具，这次刷新被跳过，meta 模式下 tools/list 为空且无错误。
func TestMetaToolsRegisteredAfterToolsAlreadyExist(t *testing.T) {
	echoURL := startDownstream(t, echoTool("echo"))

	e := newEnv(t, []downstream.Spec{remoteSpec("alpha", echoURL)})

	// 模拟 serve 的启动顺序：上游工具已经进了快照之后，才挂 meta-tool。
	e.svc.AttachEmbedding(nil)

	got := make(map[string]bool)
	for _, tool := range e.listTools(t) {
		got[tool.Name] = true
	}

	for _, want := range []string{
		"hub__list_categories",
		"hub__list_servers",
		"hub__list_tools",
		"hub__search_tools",
		"hub__tool",
	} {
		if !got[want] {
			t.Errorf("meta-tool %q 未注册，实际列表: %v", want, toolNames(e.listTools(t)))
		}
	}

	// SetTools 是全量替换：注册 meta-tool 不能让上游工具消失。
	if !got["alpha__echo"] {
		t.Errorf("上游工具 alpha__echo 丢失，实际列表: %v", toolNames(e.listTools(t)))
	}
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func toolNames(tools []mcp.Tool) []string {
	out := make([]string, 0, len(tools))
	for _, tool := range tools {
		out = append(out, tool.Name)
	}
	return out
}
