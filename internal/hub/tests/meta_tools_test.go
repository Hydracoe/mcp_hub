package tests

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/hydra/mcp-hub/internal/store/servers"
)

/*
这一组钉住"给 AI 的浏览入口只列能调的工具"。

背景：registry 的快照里有两份列表——tools/routes 是给 AI 的（只含人工启用的），
entries 是给管理界面的（额外含"缓存里有但开关关着"的）。meta 侧若直接遍历
entries，就会把未启用的工具推给 AI：AI 拿到 exposedName（甚至完整 schema）
去调用，只会得到 "tool not found"，而且归因不到"未启用"上。

实测里就是这样撞见的：上游新增工具后 hub__list_tools 把它列了出来，
而 all 模式的 tools/list 里根本没有它。
*/

// 未启用的工具对 AI 不可见——两个 meta 入口都要一致。
func TestMetaToolsHideDisabledTool(t *testing.T) {
	down := startDownstream(t,
		toolSpec{name: "ping", desc: "Ping the upstream and return pong"},
	)
	e := newMetaEnv(t, nil)
	ctx := context.Background()

	spec := remoteSpec("dk", down)
	if err := e.store.Servers.Upsert(ctx, servers.Config{Spec: spec, Enabled: true}); err != nil {
		t.Fatalf("Upsert 失败: %v", err)
	}
	srv, err := e.store.Servers.GetByKey(ctx, "dk")
	if err != nil {
		t.Fatal(err)
	}
	e.svc.RefreshAll(ctx)

	// 探活落库后默认是禁用的（新工具须人工启用）。
	rows, err := e.store.Servers.ListToolsByServer(ctx, srv.Spec.ID)
	if err != nil || len(rows) != 1 || rows[0].Enabled {
		t.Fatalf("前置状态异常（应为已缓存且禁用）: %+v err=%v", rows, err)
	}

	// 1) list_tools 不列未启用的工具。
	var listed []map[string]any
	metaJSON(t, e.callTool(t, "hub__list_tools", `{"server":"dk"}`), &listed)
	if len(listed) != 0 {
		t.Errorf("未启用的工具不该出现在 hub__list_tools: %+v", listed)
	}

	// 2) tool 不给未启用工具的定义——给了 schema 却调不动，比说没找到更糟。
	got := e.callTool(t, "hub__tool", `{"name":"dk__ping"}`)
	if !got.IsError {
		t.Errorf("未启用的工具不该能取到定义: %s", textOf(t, got))
	}
	if strings.Contains(textOf(t, got), "inputSchema") {
		t.Errorf("未启用的工具不该返回 inputSchema: %s", textOf(t, got))
	}

	// 3) 人工启用后两个入口同时可见。
	if err := e.svc.SetToolEnabled(ctx, srv.Spec.ID, "ping", true); err != nil {
		t.Fatalf("启用失败: %v", err)
	}

	listed = nil
	metaJSON(t, e.callTool(t, "hub__list_tools", `{"server":"dk"}`), &listed)
	if len(listed) != 1 || listed[0]["exposedName"] != "dk__ping" {
		t.Errorf("启用后 hub__list_tools = %+v, want [dk__ping]", listed)
	}

	var def map[string]any
	metaJSON(t, e.callTool(t, "hub__tool", `{"name":"dk__ping"}`), &def)
	if def["exposedName"] != "dk__ping" || def["inputSchema"] == nil {
		t.Errorf("启用后应能取到定义: %+v", def)
	}

	// 4) 再禁用回去，两个入口同时收回。
	if err := e.svc.SetToolEnabled(ctx, srv.Spec.ID, "ping", false); err != nil {
		t.Fatalf("禁用失败: %v", err)
	}
	listed = nil
	metaJSON(t, e.callTool(t, "hub__list_tools", `{"server":"dk"}`), &listed)
	if len(listed) != 0 {
		t.Errorf("再次禁用后仍出现在 hub__list_tools: %+v", listed)
	}
	if got := e.callTool(t, "hub__tool", `{"name":"dk__ping"}`); !got.IsError {
		t.Errorf("再次禁用后仍能取到定义: %s", textOf(t, got))
	}
}

// 管理接口与 AI 入口的差异是有意的：管理侧必须看得到禁用的工具，
// 否则"被禁用的工具"就成了看不见也放不回来的黑洞。
func TestDisabledToolStillVisibleToAdmin(t *testing.T) {
	down := startDownstream(t, toolSpec{name: "ping", desc: "Ping the upstream"})
	e := newMetaEnv(t, nil)
	ctx := context.Background()

	if err := e.store.Servers.Upsert(ctx, servers.Config{Spec: remoteSpec("dk", down), Enabled: true}); err != nil {
		t.Fatalf("Upsert 失败: %v", err)
	}
	srv, err := e.store.Servers.GetByKey(ctx, "dk")
	if err != nil {
		t.Fatal(err)
	}
	e.svc.RefreshAll(ctx)

	// 先确认 AI 侧看不到。
	var listed []map[string]any
	metaJSON(t, e.callTool(t, "hub__list_tools", `{"server":"dk"}`), &listed)
	if len(listed) != 0 {
		t.Fatalf("前置失败：AI 侧不该看到未启用工具: %+v", listed)
	}

	// 管理侧的快照里它仍在，且 enabled=false——这是"有地方点回来"的前提。
	var found bool
	for _, entry := range e.svc.Registry().Current().ToolEntries() {
		if entry.Exposed != "dk__ping" {
			continue
		}
		found = true
		if entry.Enabled {
			t.Errorf("管理侧应看到 enabled=false: %+v", entry)
		}
	}
	if !found {
		t.Fatalf("管理侧应仍能看见被禁用的工具（否则没有地方把它放回来）")
	}

	// 落库状态一致。
	rows, err := e.store.Servers.ListToolsByServer(ctx, srv.Spec.ID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("缓存行应保留: %+v err=%v", rows, err)
	}
}

// 没有任何可调用工具的 server 对 AI 不存在：hub__list_servers 不列它，
// hub__list_categories 的计数也只数"AI 可用"口径（而不是库里的原始计数）。
// 管理接口不受影响——那边必须看得到"启用中但零可用工具"的 server，
// 否则就没有地方把工具放回来。
func TestMetaToolsHideServerWithoutAvailableTools(t *testing.T) {
	down := startDownstream(t,
		toolSpec{name: "ping", desc: "Ping the upstream"},
		toolSpec{name: "pong", desc: "Pong the upstream"},
	)
	e := newMetaEnv(t, nil)
	ctx := context.Background()

	cat, err := e.svc.Store().Categories.Create(ctx, "实验室", "lab", "")
	if err != nil {
		t.Fatalf("建分类失败: %v", err)
	}
	res, err := e.svc.ApplyServer(ctx, servers.Config{
		Spec:       remoteSpec("dk", down),
		CategoryID: cat.ID,
		Enabled:    true,
	})
	if err != nil {
		t.Fatalf("ApplyServer 失败: %v", err)
	}
	e.svc.RefreshAll(ctx)

	// 探活发现的两个工具都默认禁用 → server 此刻零可用工具。
	listed := listServersByCategory(t, e, "实验室")
	if len(listed) != 0 {
		t.Errorf("零可用工具的 server 不该出现在 hub__list_servers: %+v", listed)
	}
	assertCategoryCount(t, e, cat.ID, 0, 0)

	// 启用一个工具：server 与计数立刻回来。
	if err := e.svc.SetToolEnabled(ctx, res.ID, "ping", true); err != nil {
		t.Fatalf("启用工具失败: %v", err)
	}
	listed = listServersByCategory(t, e, "实验室")
	if len(listed) != 1 || listed[0]["key"] != "dk" {
		t.Errorf("启用工具后 server 应回到 hub__list_servers: %+v", listed)
	}
	assertCategoryCount(t, e, cat.ID, 1, 1)

	// 再禁用：server 重新对 AI 隐藏，计数归零。
	if err := e.svc.SetToolEnabled(ctx, res.ID, "ping", false); err != nil {
		t.Fatalf("禁用工具失败: %v", err)
	}
	listed = listServersByCategory(t, e, "实验室")
	if len(listed) != 0 {
		t.Errorf("再次禁用后 server 应从 hub__list_servers 消失: %+v", listed)
	}
	assertCategoryCount(t, e, cat.ID, 0, 0)
}

// listServersByCategory 调 hub__list_servers 并返回解析后的列表。
func listServersByCategory(t *testing.T, e *env, category string) []map[string]any {
	t.Helper()
	var out []map[string]any
	metaJSON(t, e.callTool(t, "hub__list_servers", fmt.Sprintf(`{"category":%q}`, category)), &out)
	return out
}

// assertCategoryCount 断言 hub__list_categories 里某分类的 AI 可用口径计数。
// JSON 数字反序列化后是 float64，在这里统一折算比较。
func assertCategoryCount(t *testing.T, e *env, catID string, wantServers, wantTools int) {
	t.Helper()
	var cats []map[string]any
	metaJSON(t, e.callTool(t, "hub__list_categories", `{}`), &cats)
	for _, c := range cats {
		if c["id"] != catID {
			continue
		}
		if got, _ := c["serverCount"].(float64); got != float64(wantServers) {
			t.Errorf("serverCount = %v, want %d", c["serverCount"], wantServers)
		}
		if got, _ := c["toolCount"].(float64); got != float64(wantTools) {
			t.Errorf("toolCount = %v, want %d", c["toolCount"], wantTools)
		}
		return
	}
	t.Fatalf("分类 %s 不在 hub__list_categories 结果里: %+v", catID, cats)
}
