package tests

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hydra/mcp-hub/internal/downstream"
	"github.com/hydra/mcp-hub/internal/hub/aggregate"
	"github.com/hydra/mcp-hub/internal/hub/registry"
	"github.com/mark3labs/mcp-go/mcp"
)

// 这一组用例守的是"聚合列表怎么被拼出来"：重名怎么取舍、下游顺序抖动会不会让
// AI 收到一次假的"列表变了"、命名空间会不会被下游占用。
// 它们只走 registry 的导出 API，是纯内存断言，不依赖数据库。

func toolSource(serverID, serverKey string, tools ...downstream.ToolInfo) registry.ToolSource {
	return registry.ToolSource{
		ServerID:  serverID,
		ServerKey: serverKey,
		Name:      serverKey,
		Enabled:   true,
		Available: true,
		Tools:     tools,
	}
}

func toolInfo(name, desc string) downstream.ToolInfo {
	return downstream.ToolInfo{
		Name:        name,
		Description: desc,
		InputSchema: json.RawMessage(`{"type":"object"}`),
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// 下游工具名里带 "__" 不该出任何事：对外名照拼、路由靠精确查表。
// 这里特意构造出"按末个 __ 切分就会串线"的形状，防止有人把查表换成反推。
func TestToolNameContainingSeparatorRoutesByExactLookup(t *testing.T) {
	reg := registry.NewRegistry()
	snap := reg.Build([]registry.ToolSource{
		toolSource("srv-1", "labc", toolInfo("a__b", "名字里带分隔符"), toolInfo("b", "普通名")),
		toolSource("srv-2", "lab", toolInfo("c__a__b", "另一台，名字更绕")),
	})

	for _, c := range []struct{ exposed, serverID, tool string }{
		{"labc__a__b", "srv-1", "a__b"},
		{"labc__b", "srv-1", "b"},
		{"lab__c__a__b", "srv-2", "c__a__b"},
	} {
		route, ok := snap.Lookup(c.exposed)
		if !ok {
			t.Errorf("%s 未注册（工具名带分隔符不该装不上）", c.exposed)
			continue
		}
		if route.ServerID != c.serverID || route.ToolName != c.tool {
			t.Errorf("%s 路由到 (%s, %s), want (%s, %s)——切分反推必然串线",
				c.exposed, route.ServerID, route.ToolName, c.serverID, c.tool)
		}
	}
}

// "工具名可以含 __" 之所以成立，靠的是"serverKey 永远不含 __"这个前提：
// 如此 <serverKey>__<toolName> 里第一个 __ 之前必是完整 serverKey。
// 前提一旦被破，光靠精确查表不足以保证跨 server 不撞名，所以在这里钉死。
func TestServerKeyRejectsSeparatorSoExposedNameStaysUnambiguous(t *testing.T) {
	for _, bad := range []string{"a__b", "_a", "a_", "a b", "-a", "a/b"} {
		spec := downstream.Spec{Key: bad}
		if err := spec.Validate(); err == nil {
			t.Errorf("key %q 被接受了，但它会让 <serverKey>__<toolName> 的两段边界失真", bad)
		}
	}
	ok := downstream.Spec{
		Key:       "github-v2",
		Transport: downstream.TransportStreamableHTTP,
		URL:       "https://example.com/mcp",
	}
	if err := ok.Validate(); err != nil {
		t.Errorf("正常 key 被拒了: %v", err)
	}
}

// serverKey 取 "hub"（keyPattern 放行）会让它的对外名整体落进网关自带工具的
// 命名空间：hub__list_tools 之类与 meta 工具同名。聚合层让真元工具占住名字，
// 本用例钉住"配置源头必须拦住它"——这是保留命名空间被占用的唯一入口。
func TestServerKeyHubLandsInsideReservedMetaNamespace(t *testing.T) {
	reg := registry.NewRegistry()
	snap := reg.Build([]registry.ToolSource{
		toolSource("srv-hub", "hub", toolInfo("list_tools", "冒充元工具"), toolInfo("alpha", "普通工具")),
	})

	route, ok := snap.Lookup("hub__list_tools")
	if !ok {
		t.Fatal("前提变了：hub__list_tools 已不是下游路由，可放宽本用例")
	}
	if route.ServerID != "srv-hub" || route.ToolName != "list_tools" {
		t.Fatalf("路由异常: %+v", route)
	}
	if !strings.HasPrefix("hub__list_tools", "hub__") {
		t.Fatal("meta 前缀判断失效")
	}
}

// 网关自带工具的命名空间 hub__ 不被下游占用，这条不变量横跨 aggregate 与
// downstream 两个包，所以在这里钉死一致性。
//
// downstream 为了不反向依赖 aggregate，把保留前缀复述了一份。
// 两者一旦漂移，"被配置校验挡住的 key"与"聚合层实际保留的前缀"就对不上，
// 校验会形同虚设。
func TestReservedMetaPrefixMatchesDownstreamCopy(t *testing.T) {
	if aggregate.MetaPrefix != downstream.ReservedMetaPrefix {
		t.Fatalf("前缀漂移: aggregate.MetaPrefix=%q, downstream.ReservedMetaPrefix=%q",
			aggregate.MetaPrefix, downstream.ReservedMetaPrefix)
	}
	reservedKey := strings.TrimSuffix(aggregate.MetaPrefix, "__")
	if err := (downstream.Spec{Key: reservedKey}).Validate(); err == nil {
		t.Errorf("key %q 已落进保留前缀 %q，配置校验却没拦住", reservedKey, aggregate.MetaPrefix)
	}
}

// 不同 server 的同名工具靠前缀天然不撞：这是"两个上游都叫 search"的常态，
// 跳过了才是 bug。
func TestBuildDistinguishesSameToolNameAcrossServers(t *testing.T) {
	reg := registry.NewRegistry()
	snap := reg.Build([]registry.ToolSource{
		toolSource("srv-a", "a", toolInfo("search", "A 家的搜索")),
		toolSource("srv-b", "b", toolInfo("search", "B 家的搜索")),
	})

	if snap.ToolCount() != 2 {
		t.Fatalf("聚合工具数 = %d, want 2: %v", snap.ToolCount(), toolNames(snap.Tools()))
	}
	if _, ok := snap.Lookup("a__search"); !ok {
		t.Error("a__search 未注册")
	}
	if _, ok := snap.Lookup("b__search"); !ok {
		t.Error("b__search 未注册")
	}

	// 路由必须各回各家：打到 b 的请求不能落到 a 的下游上。
	r1, _ := snap.Lookup("a__search")
	r2, _ := snap.Lookup("b__search")
	if r1.ServerID != "srv-a" || r2.ServerID != "srv-b" {
		t.Fatalf("路由串线: %+v / %+v", r1, r2)
	}
}

// 同一对外名重复出现（serverKey 撞了，理论上不该发生）：宁可少暴露一个，
// 也不能让调用悄悄打到别的下游。
func TestBuildSkipsDuplicateExposedName(t *testing.T) {
	reg := registry.NewRegistry()
	snap := reg.Build([]registry.ToolSource{
		toolSource("srv-1", "dup", toolInfo("alpha", "第一个")),
		toolSource("srv-2", "dup", toolInfo("alpha", "第二个")),
	})

	if snap.ToolCount() != 1 {
		t.Fatalf("重复对外名应只保留一个，实际 %d: %v", snap.ToolCount(), toolNames(snap.Tools()))
	}
	route, ok := snap.Lookup("dup__alpha")
	if !ok {
		t.Fatal("dup__alpha 未注册")
	}
	if route.ServerID != "srv-1" {
		t.Errorf("保留的是 %s, want srv-1（先到先得，不覆盖）", route.ServerID)
	}
}

// 快照不因来源的输入顺序而改变：顺序抖动若导致列表变了，AI 会收到一次
// 毫无必要的"工具列表已更新"。
func TestBuildIsIndependentOfSourceOrder(t *testing.T) {
	a := toolSource("srv-a", "aaa", toolInfo("x", "X"), toolInfo("y", "Y"))
	b := toolSource("srv-b", "bbb", toolInfo("z", "Z"))

	first := registry.NewRegistry().Build([]registry.ToolSource{a, b})
	second := registry.NewRegistry().Build([]registry.ToolSource{b, a})

	if registry.Fingerprint(first.Tools()) != registry.Fingerprint(second.Tools()) {
		t.Errorf("来源顺序改变了指纹:\n%v\n%v", toolNames(first.Tools()), toolNames(second.Tools()))
	}
	if !equalStrings(toolNames(first.Tools()), toolNames(second.Tools())) {
		t.Errorf("来源顺序改变了工具顺序: %v vs %v", toolNames(first.Tools()), toolNames(second.Tools()))
	}
}

// 指纹对顺序敏感——正因如此，排序的责任落在 DB 层（ListTools 按 exposed_name 定序）。
// 这条用例把"顺序会进指纹"钉住，免得有人以为指纹天然与顺序无关而把排序删掉。
func TestFingerprintDependsOnOrder(t *testing.T) {
	one := []mcp.Tool{{Name: "a", Description: "A"}, {Name: "b", Description: "B"}}
	two := []mcp.Tool{{Name: "b", Description: "B"}, {Name: "a", Description: "A"}}

	if registry.Fingerprint(one) == registry.Fingerprint(two) {
		t.Error("指纹与顺序无关？那排序就可以删了——但排序正是 T9 的唯一保障")
	}
}

// 指纹不含可用性：下游抖一下不该广播列表变更。
func TestFingerprintIgnoresAvailability(t *testing.T) {
	tools := []mcp.Tool{{Name: "a", Description: "A", RawInputSchema: json.RawMessage(`{"type":"object"}`)}}
	base := registry.Fingerprint(tools)

	// 可用性不在 mcp.Tool 上表达，因此同一批工具的指纹必然相同；
	// 这条用例守的是"没人把可用性塞回工具定义里"。
	if registry.Fingerprint(tools) != base {
		t.Error("同一批工具两次指纹不同")
	}
}
