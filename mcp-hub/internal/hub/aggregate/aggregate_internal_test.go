package aggregate

import (
	"context"
	"encoding/json"
	"sort"
	"testing"

	"github.com/hydra/mcp-hub/internal/downstream"
	"github.com/hydra/mcp-hub/internal/hub/registry"
	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

// 这一组守的是"网关自带工具的命名空间 hub__ 不被下游占用"。
// 对外名是 <serverKey>__<toolName>，keyPattern 放行 "hub" 时该 server 的工具会以
// hub__ 开头，恰好满足 meta 模式挑元工具的判断，于是冒充成 hub__list_tools 之类露给 AI。

// metaTool 造一个最小可用的元工具（真元工具由 SetMetaTools 注册进来）。
func metaTool(name, desc string) mcpserver.ServerTool {
	return mcpserver.ServerTool{
		Tool: mcp.NewTool(name, mcp.WithDescription(desc)),
		Handler: func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return mcp.NewToolResultText("ok"), nil
		},
	}
}

// registeredTools 返回端点上实际注册的工具，按名字定序。
// 这是 mcp-go 的 tools/list 交给过滤器链的那份输入（过滤发生在分页之前）。
func registeredTools(a *Aggregator) []mcp.Tool {
	list := a.MCP().ListTools()
	out := make([]mcp.Tool, 0, len(list))
	for _, st := range list {
		out = append(out, st.Tool)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// toolNames 把工具列表压成名字列表，便于断言。
func toolNames(tools []mcp.Tool) []string {
	out := make([]string, 0, len(tools))
	for _, t := range tools {
		out = append(out, t.Name)
	}
	return out
}

// meta 模式下，serverKey=hub 的下游工具不该冒充元工具露给 AI：
// 真元工具从不出现在路由表里，用这一点把它们摘掉。
func TestMetaFilterExcludesDownstreamToolsClaimingMetaPrefix(t *testing.T) {
	reg := registry.NewRegistry()
	sources := []registry.ToolSource{{
		ServerID: "srv-hub", ServerKey: "hub", Name: "hub",
		Enabled: true, Available: true,
		Tools: []downstream.ToolInfo{
			{Name: "list_tools", Description: "与真元工具同名", InputSchema: json.RawMessage(`{"type":"object"}`)},
			{Name: "alpha", Description: "普通工具", InputSchema: json.RawMessage(`{"type":"object"}`)},
		},
	}}
	reg.Build(sources)

	// 前提：这台 server 的工具确实占用了保留前缀，否则本用例没意义。
	if _, ok := reg.Current().Lookup("hub__list_tools"); !ok {
		t.Fatal("前提不成立：hub__list_tools 不是下游路由")
	}

	a := NewAggregator(Deps{Sources: func() ([]registry.ToolSource, bool) { return sources, true }}, reg, nil, ListModeMeta)

	got := a.metaOnlyFilter(context.Background(), registeredTools(a))
	if len(got) != 0 {
		t.Fatalf("meta 过滤后仍有 %v——下游工具冒充成元工具露给了 AI", toolNames(got))
	}
}

// 真元工具不能因为"路由表里恰好有同名条目"被摘掉：真元工具显式登记且
// 后注册占住名字，同名下游工具的其它工具也不得冒充成元工具。
func TestMetaFilterKeepsRealMetaToolWhenDownstreamSharesName(t *testing.T) {
	reg := registry.NewRegistry()
	sources := []registry.ToolSource{{
		ServerID: "srv-hub", ServerKey: "hub", Name: "hub",
		Enabled: true, Available: true,
		Tools: []downstream.ToolInfo{
			{Name: "list_tools", Description: "与真元工具同名", InputSchema: json.RawMessage(`{"type":"object"}`)},
			{Name: "alpha", Description: "普通工具", InputSchema: json.RawMessage(`{"type":"object"}`)},
		},
	}}
	reg.Build(sources)

	a := NewAggregator(Deps{Sources: func() ([]registry.ToolSource, bool) { return sources, true }}, reg, nil, ListModeMeta)
	a.SetMetaTools(func() []mcpserver.ServerTool {
		return []mcpserver.ServerTool{
			metaTool(MetaPrefix+"list_tools", "真元工具 list_tools"),
			metaTool(MetaPrefix+"search_tools", "真元工具 search_tools"),
		}
	})

	// 前提：端点上 hub__list_tools 占住名字的是真元工具本人。
	var found bool
	for _, tl := range registeredTools(a) {
		if tl.Name == MetaPrefix+"list_tools" {
			found = true
			if tl.Description != "真元工具 list_tools" {
				t.Fatalf("hub__list_tools 被下游工具占住了: %+v", tl)
			}
		}
	}
	if !found {
		t.Fatal("前提不成立：端点上没有 hub__list_tools")
	}

	got := a.metaOnlyFilter(context.Background(), registeredTools(a))
	names := toolNames(got)
	want := []string{MetaPrefix + "list_tools", MetaPrefix + "search_tools"}
	if len(names) != len(want) {
		t.Fatalf("meta 过滤后的工具 = %v, want %v", names, want)
	}
	for i, n := range want {
		if names[i] != n {
			t.Fatalf("meta 过滤后的工具 = %v, want %v", names, want)
		}
	}
}

// 一次读库失败不能把已有快照换成空列表：工具"最近一次已知"的语义
// 必须由聚合层兜住，而不是指望调用方每次都读到成功。
func TestRefreshKeepsSnapshotWhenSourcesFail(t *testing.T) {
	reg := registry.NewRegistry()
	fail := false
	sources := []registry.ToolSource{{
		ServerID: "srv-dk", ServerKey: "dk", Name: "dk",
		Enabled: true, Available: true,
		Tools: []downstream.ToolInfo{
			{Name: "ping", Description: "探活", InputSchema: json.RawMessage(`{"type":"object"}`)},
		},
	}}

	a := NewAggregator(Deps{Sources: func() ([]registry.ToolSource, bool) {
		if fail {
			return nil, false
		}
		return sources, true
	}}, reg, nil, ListModeMeta)

	before := a.reg.Current()
	if _, ok := before.Lookup("dk__ping"); !ok {
		t.Fatal("前提不成立：初始快照里没有 dk__ping")
	}

	fail = true
	a.Refresh()

	after := a.reg.Current()
	if after != before {
		t.Fatalf("读库失败后快照被替换: before=%p after=%p", before, after)
	}
	if _, ok := after.Lookup("dk__ping"); !ok {
		t.Fatal("读库失败后 dk__ping 从聚合快照里消失了")
	}
}

// 真元工具必须照常通过：把下游工具摘掉的那条判断不能误伤它们。
func TestMetaFilterKeepsRealMetaTools(t *testing.T) {
	reg := registry.NewRegistry()
	reg.Build(nil)

	a := NewAggregator(Deps{Sources: func() ([]registry.ToolSource, bool) { return nil, true }}, reg, nil, ListModeMeta)
	a.SetMetaTools(func() []mcpserver.ServerTool {
		return []mcpserver.ServerTool{metaTool(MetaPrefix+"search_tools", "搜索")}
	})

	tools := []mcp.Tool{{Name: MetaPrefix + "search_tools"}}
	got := a.metaOnlyFilter(context.Background(), tools)
	if len(got) != 1 || got[0].Name != MetaPrefix+"search_tools" {
		t.Fatalf("真元工具被误伤: %+v", got)
	}
}

// 列表路径与调用路径必须分开：列表只留元工具，调用放行被请求的那个工具。
//
// mcp-go 的 list 与 call 共用同一条过滤器链，判据是输入里有没有元工具，
// 而不是工具数量。
func TestMetaFilterSeparatesListFromCall(t *testing.T) {
	reg := registry.NewRegistry()
	sources := []registry.ToolSource{{
		ServerID: "srv-dk", ServerKey: "dk", Name: "dk",
		Enabled: true, Available: true,
		Tools: []downstream.ToolInfo{
			{Name: "ping", Description: "探活", InputSchema: json.RawMessage(`{"type":"object"}`)},
			{Name: "pong", Description: "探活", InputSchema: json.RawMessage(`{"type":"object"}`)},
		},
	}}
	reg.Build(sources)

	a := NewAggregator(Deps{Sources: func() ([]registry.ToolSource, bool) { return sources, true }}, reg, nil, ListModeMeta)
	a.SetMetaTools(func() []mcpserver.ServerTool {
		return []mcpserver.ServerTool{
			metaTool(MetaPrefix+"search_tools", "搜索"),
			metaTool(MetaPrefix+"tool", "取定义"),
		}
	})

	// 列表路径：已注册的全部工具进去，只有元工具出来。
	got := a.metaOnlyFilter(context.Background(), registeredTools(a))
	names := toolNames(got)
	want := []string{MetaPrefix + "search_tools", MetaPrefix + "tool"}
	if len(names) != len(want) || names[0] != want[0] || names[1] != want[1] {
		t.Fatalf("列表路径过滤结果 = %v, want %v", names, want)
	}

	// 调用路径：调用下游工具——必须放行，否则工具再也调不到。
	call := []mcp.Tool{{Name: "dk__ping"}}
	got = a.metaOnlyFilter(context.Background(), call)
	if len(got) != 1 || got[0].Name != "dk__ping" {
		t.Fatalf("调用下游工具被误伤: %+v", toolNames(got))
	}

	// 调用路径：调用元工具——同样放行。
	call = []mcp.Tool{{Name: MetaPrefix + "search_tools"}}
	got = a.metaOnlyFilter(context.Background(), call)
	if len(got) != 1 || got[0].Name != MetaPrefix+"search_tools" {
		t.Fatalf("调用元工具被误伤: %+v", toolNames(got))
	}
}

// 单工具场景：端点上只注册了一个工具时，列表与调用的输入无法区分，
// 过滤器一律放行——宁可多列一个工具，也不能让工具调不到。
func TestMetaFilterSingleToolScenarioUnchanged(t *testing.T) {
	t.Run("单个元工具", func(t *testing.T) {
		reg := registry.NewRegistry()
		reg.Build(nil)

		a := NewAggregator(Deps{Sources: func() ([]registry.ToolSource, bool) { return nil, true }}, reg, nil, ListModeMeta)
		a.SetMetaTools(func() []mcpserver.ServerTool {
			return []mcpserver.ServerTool{metaTool(MetaPrefix+"search_tools", "搜索")}
		})

		got := a.metaOnlyFilter(context.Background(), registeredTools(a))
		if len(got) != 1 || got[0].Name != MetaPrefix+"search_tools" {
			t.Fatalf("唯一的元工具不该被摘掉: %+v", toolNames(got))
		}
	})

	t.Run("单个下游工具", func(t *testing.T) {
		reg := registry.NewRegistry()
		sources := []registry.ToolSource{{
			ServerID: "srv-dk", ServerKey: "dk", Name: "dk",
			Enabled: true, Available: true,
			Tools: []downstream.ToolInfo{
				{Name: "ping", Description: "探活", InputSchema: json.RawMessage(`{"type":"object"}`)},
			},
		}}
		reg.Build(sources)

		// 元工具还没注册（启动窗口）：列表与调用的输入一样，一律放行。
		a := NewAggregator(Deps{Sources: func() ([]registry.ToolSource, bool) { return sources, true }}, reg, nil, ListModeMeta)

		got := a.metaOnlyFilter(context.Background(), registeredTools(a))
		if len(got) != 1 || got[0].Name != "dk__ping" {
			t.Fatalf("单工具场景行为变了: %+v", toolNames(got))
		}
	})
}
