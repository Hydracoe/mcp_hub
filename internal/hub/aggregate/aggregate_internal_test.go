package aggregate

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/hydra/mcp-hub/internal/downstream"
	"github.com/hydra/mcp-hub/internal/hub/registry"
	"github.com/mark3labs/mcp-go/mcp"
)

// 这一组守的是"网关自带工具的命名空间 hub__ 不被下游占用"。
// 起因：对外名是 <serverKey>__<toolName>，而 keyPattern 放行 "hub"——
// 那台 server 的全部工具都会以 hub__ 开头，恰好满足 meta 模式挑元工具的
// 前缀判断，于是冒充成 hub__list_tools 之类露给 AI。
//
// 保留前缀与 downstream 的一致性用例在 ../tests 黑盒目录。

// meta 模式下，即便真有一台 serverKey=hub 的存量 server（配置校验上线前
// 建的），它的工具也不该冒充元工具露给 AI：真元工具从不出现在路由表里，
// 用这一点把它们摘掉。
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

	a := NewAggregator(Deps{Sources: func() []registry.ToolSource { return sources }}, reg, nil, ListModeMeta)

	got := a.metaOnlyFilter(context.Background(), reg.Current().Tools())
	if len(got) != 0 {
		names := make([]string, 0, len(got))
		for _, tl := range got {
			names = append(names, tl.Name)
		}
		t.Fatalf("meta 过滤后仍有 %v——下游工具冒充成元工具露给了 AI", names)
	}
}

// 真元工具必须照常通过：把下游工具摘掉的那条判断不能误伤它们。
func TestMetaFilterKeepsRealMetaTools(t *testing.T) {
	reg := registry.NewRegistry()
	reg.Build(nil)

	a := NewAggregator(Deps{Sources: func() []registry.ToolSource { return nil }}, reg, nil, ListModeMeta)

	tools := []mcp.Tool{{Name: MetaPrefix + "search_tools"}}
	got := a.metaOnlyFilter(context.Background(), tools)
	if len(got) != 1 || got[0].Name != MetaPrefix+"search_tools" {
		t.Fatalf("真元工具被误伤: %+v", got)
	}
}
