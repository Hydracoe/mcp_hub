package tests

import (
	"context"
	"testing"
	"time"

	"github.com/hydra/mcp-hub/internal/downstream"
	"github.com/hydra/mcp-hub/internal/store/servers"
	"github.com/hydra/mcp-hub/internal/store/toolchanges"
)

// probe 造一个成功探活结果，工具名可定制。
func probe(tools ...string) *downstream.ProbeResult {
	infos := make([]downstream.ToolInfo, 0, len(tools))
	for _, n := range tools {
		infos = append(infos, downstream.ToolInfo{Name: n, Description: "desc-" + n})
	}
	return &downstream.ProbeResult{
		OK:              true,
		ProtocolVersion: "2026-07-28",
		ServerName:      "fake-mcp",
		ToolCount:       len(tools),
		Tools:           infos,
		Latency:         downstream.StageLatency{Total: 5},
		CheckedAt:       time.Now(),
	}
}

// 按名取工具行的小辅助。
func toolByName(t *testing.T, s *servers.Store, ctx context.Context, serverID, name string) servers.Tool {
	t.Helper()
	rows, err := s.ListToolsByServer(ctx, serverID)
	if err != nil {
		t.Fatalf("ListToolsByServer 失败: %v", err)
	}
	for _, r := range rows {
		if r.Name == name {
			return r
		}
	}
	t.Fatalf("工具 %s 不存在，实际 %v", name, rows)
	return servers.Tool{}
}

// 新探活发现的工具必须默认禁用——"默认全开"会让上游一上新工具，
// AI 立刻就能调到一个没人评审过的能力。
func TestNewToolsDefaultDisabled(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	if err := st.Servers.Upsert(ctx, servers.Config{Spec: testSpec(), Enabled: true}); err != nil {
		t.Fatal(err)
	}
	srv, _ := st.Servers.GetByKey(ctx, "fake")
	if err := st.Servers.SaveProbe(ctx, srv.Spec.ID, probe("alpha", "beta")); err != nil {
		t.Fatal(err)
	}

	tools, err := st.Servers.ListToolsByServer(ctx, srv.Spec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 2 {
		t.Fatalf("工具数 = %d, want 2", len(tools))
	}
	for _, tl := range tools {
		if tl.Enabled {
			t.Errorf("新工具 %s 应默认禁用", tl.Name)
		}
	}

	// 首轮探活应留两条 added，且明细写明"默认禁用"。
	items, total, err := st.ToolChanges.List(ctx, toolchanges.Query{ServerID: srv.Spec.ID})
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 {
		t.Fatalf("变更记录数 = %d, want 2", total)
	}
	for _, c := range items {
		if c.Action != toolchanges.ActionAdded || c.Actor != toolchanges.ActorSystem {
			t.Errorf("记录异常: %+v", c)
		}
	}
}

// 人工启停：状态翻转、未知工具返回 false，并留 enabled/disabled 记录。
func TestSetToolEnabled(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	if err := st.Servers.Upsert(ctx, servers.Config{Spec: testSpec(), Enabled: true}); err != nil {
		t.Fatal(err)
	}
	srv, _ := st.Servers.GetByKey(ctx, "fake")
	if err := st.Servers.SaveProbe(ctx, srv.Spec.ID, probe("alpha")); err != nil {
		t.Fatal(err)
	}

	ok, err := st.Servers.SetToolEnabled(ctx, srv.Spec.ID, "alpha", true)
	if err != nil || !ok {
		t.Fatalf("启用失败: ok=%v err=%v", ok, err)
	}
	if !toolByName(t, st.Servers, ctx, srv.Spec.ID, "alpha").Enabled {
		t.Error("启用后回读仍为禁用")
	}

	// 未知工具不影响任何行。
	ok, err = st.Servers.SetToolEnabled(ctx, srv.Spec.ID, "ghost", true)
	if err != nil {
		t.Fatalf("未知工具不应报错: %v", err)
	}
	if ok {
		t.Error("未知工具应返回 false")
	}

	// server 停用后，启用单个工具必须在同一条 UPDATE 里被拒绝：并发窗口
	// 里 Service 的预检查可能已经过期，不能靠它兜底。
	if _, err := st.Servers.SetEnabledCascade(ctx, srv.Spec.ID, false); err != nil {
		t.Fatalf("停用 server 失败: %v", err)
	}
	ok, err = st.Servers.SetToolEnabled(ctx, srv.Spec.ID, "alpha", true)
	if err != nil {
		t.Fatalf("停用 server 后启用工具不应报错: %v", err)
	}
	if ok {
		t.Error("停用 server 后不应能启用工具")
	}
	if toolByName(t, st.Servers, ctx, srv.Spec.ID, "alpha").Enabled {
		t.Error("停用 server 后工具仍被启用")
	}

	ok, err = st.Servers.SetToolEnabled(ctx, srv.Spec.ID, "alpha", false)
	if err != nil || !ok {
		t.Fatalf("禁用失败: ok=%v err=%v", ok, err)
	}
	if toolByName(t, st.Servers, ctx, srv.Spec.ID, "alpha").Enabled {
		t.Error("禁用后回读仍为启用")
	}

	// 存储层只翻转状态，不写审计行——enabled/disabled 留痕是 Service 层
	// 的职责（要带 actor）。这里只有首轮探活写的 1 条 added。
	items, total, _ := st.ToolChanges.List(ctx, toolchanges.Query{ServerID: srv.Spec.ID, Limit: 10})
	if total != 1 || items[0].Action != toolchanges.ActionAdded {
		t.Fatalf("记录异常: total=%d items=%+v", total, items)
	}
}

// 再次探活：id 与人工启用状态必须跨周期保留（向量按 id 增量嵌入，
// enabled 是人工决定，都不该被探活冲掉）；新工具仍默认禁用。
func TestReprobeKeepsIDAndEnabled(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	if err := st.Servers.Upsert(ctx, servers.Config{Spec: testSpec(), Enabled: true}); err != nil {
		t.Fatal(err)
	}
	srv, _ := st.Servers.GetByKey(ctx, "fake")
	if err := st.Servers.SaveProbe(ctx, srv.Spec.ID, probe("alpha", "beta")); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Servers.SetToolEnabled(ctx, srv.Spec.ID, "alpha", true); err != nil {
		t.Fatal(err)
	}
	before := toolByName(t, st.Servers, ctx, srv.Spec.ID, "alpha")

	// 第二轮：alpha/beta 还在，新增 gamma。
	if err := st.Servers.SaveProbe(ctx, srv.Spec.ID, probe("alpha", "beta", "gamma")); err != nil {
		t.Fatal(err)
	}

	after := toolByName(t, st.Servers, ctx, srv.Spec.ID, "alpha")
	if after.ID != before.ID {
		t.Errorf("alpha 的 id 不稳定: %q → %q", before.ID, after.ID)
	}
	if !after.Enabled {
		t.Error("人工启用状态应跨探活保留")
	}
	if toolByName(t, st.Servers, ctx, srv.Spec.ID, "gamma").Enabled {
		t.Error("第二轮新发现的 gamma 应默认禁用")
	}
}

// 上游删掉一个工具：缓存行删除，并写 removed 记录。
func TestRemovedToolDropsCacheRow(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	spec := testSpec()

	if err := st.Servers.Upsert(ctx, servers.Config{Spec: spec, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	srv, _ := st.Servers.GetByKey(ctx, "fake")
	if err := st.Servers.SaveProbe(ctx, srv.Spec.ID, probe("alpha", "beta")); err != nil {
		t.Fatal(err)
	}

	// beta 被上游撤掉。
	if err := st.Servers.SaveProbe(ctx, srv.Spec.ID, probe("alpha")); err != nil {
		t.Fatal(err)
	}

	if rows, _ := st.Servers.ListToolsByServer(ctx, srv.Spec.ID); len(rows) != 1 || rows[0].Name != "alpha" {
		t.Fatalf("beta 缓存行应删除，实际 %v", rows)
	}

	items, total, _ := st.ToolChanges.List(ctx, toolchanges.Query{ServerID: srv.Spec.ID, Limit: 10})
	if total < 1 {
		t.Fatal("应至少有一条 removed 记录")
	}
	found := false
	for i := range items {
		if items[i].ToolName == "beta" && items[i].Action == toolchanges.ActionRemoved {
			found = true
		}
	}
	if !found {
		t.Fatalf("缺少 beta 的 removed 记录: %+v", items)
	}
}

// 工具描述变化：行还是同一个（id 不变），只多一条 updated。
func TestToolMetadataChangeWritesUpdated(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	if err := st.Servers.Upsert(ctx, servers.Config{Spec: testSpec(), Enabled: true}); err != nil {
		t.Fatal(err)
	}
	srv, _ := st.Servers.GetByKey(ctx, "fake")
	if err := st.Servers.SaveProbe(ctx, srv.Spec.ID, probe("alpha")); err != nil {
		t.Fatal(err)
	}
	before := toolByName(t, st.Servers, ctx, srv.Spec.ID, "alpha")

	changed := probe("alpha")
	changed.Tools[0].Description = "全新的描述"
	if err := st.Servers.SaveProbe(ctx, srv.Spec.ID, changed); err != nil {
		t.Fatal(err)
	}

	after := toolByName(t, st.Servers, ctx, srv.Spec.ID, "alpha")
	if after.ID != before.ID {
		t.Error("描述变化不应换 id")
	}
	if after.Description != "全新的描述" {
		t.Errorf("描述未更新: %q", after.Description)
	}

	items, _, _ := st.ToolChanges.List(ctx, toolchanges.Query{ServerID: srv.Spec.ID, Limit: 10})
	found := false
	for _, c := range items {
		if c.ToolName == "alpha" && c.Action == toolchanges.ActionUpdated {
			found = true
		}
	}
	if !found {
		t.Errorf("缺少 updated 记录: %+v", items)
	}
}

// 引导名单是创建向导勾选的初始启用集合：名单内的新工具首次探活即启用，
// 名单外仍默认禁用；名单消费一次后即失效。
func TestBootEnabledToolsAppliedOnce(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	if err := st.Servers.Upsert(ctx, servers.Config{
		Spec:         testSpec(),
		Enabled:      true,
		EnabledTools: []string{"beta", "", " alpha "},
	}); err != nil {
		t.Fatal(err)
	}
	srv, _ := st.Servers.GetByKey(ctx, "fake")
	if err := st.Servers.SaveProbe(ctx, srv.Spec.ID, probe("alpha", "beta", "gamma")); err != nil {
		t.Fatal(err)
	}

	if !toolByName(t, st.Servers, ctx, srv.Spec.ID, "alpha").Enabled {
		t.Error("引导名单内的新工具应直接启用（含空白清洗）")
	}
	if !toolByName(t, st.Servers, ctx, srv.Spec.ID, "beta").Enabled {
		t.Error("引导名单内的新工具应直接启用")
	}
	if toolByName(t, st.Servers, ctx, srv.Spec.ID, "gamma").Enabled {
		t.Error("引导名单外的新工具应默认禁用")
	}

	// 名单是一次性的：第二轮探活新发现的工具不再受它照顾。
	if err := st.Servers.SaveProbe(ctx, srv.Spec.ID, probe("alpha", "beta", "gamma", "delta")); err != nil {
		t.Fatal(err)
	}
	if toolByName(t, st.Servers, ctx, srv.Spec.ID, "delta").Enabled {
		t.Error("引导名单只消费一次，delta 应默认禁用")
	}
}

// 引导名单等得起：创建后先改配置、探活一直失败，名单都不能丢——
// 首次成功的探活到来时，勾选必须兑现。
func TestBootEnabledToolsSurviveEditsAndFailures(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	if err := st.Servers.Upsert(ctx, servers.Config{
		Spec:         testSpec(),
		Enabled:      true,
		EnabledTools: []string{"alpha"},
	}); err != nil {
		t.Fatal(err)
	}

	// 编辑配置（不带 EnabledTools）：不得重建或清除名单。
	if err := st.Servers.Upsert(ctx, servers.Config{Spec: testSpec(), Enabled: true}); err != nil {
		t.Fatal(err)
	}

	// 探活失败只写状态，不动工具缓存，也不消费名单。
	srv, _ := st.Servers.GetByKey(ctx, "fake")
	failed := probe()
	failed.OK = false
	if err := st.Servers.SaveProbe(ctx, srv.Spec.ID, failed); err != nil {
		t.Fatal(err)
	}

	if err := st.Servers.SaveProbe(ctx, srv.Spec.ID, probe("alpha", "beta")); err != nil {
		t.Fatal(err)
	}
	if !toolByName(t, st.Servers, ctx, srv.Spec.ID, "alpha").Enabled {
		t.Error("编辑与失败探活后，引导名单应仍然生效")
	}
	if toolByName(t, st.Servers, ctx, srv.Spec.ID, "beta").Enabled {
		t.Error("名单外的工具应默认禁用")
	}
}

// 上游把工具全撤光：缓存必须清空（NOT IN 空集的边界）。
func TestEmptyProbeClearsAllTools(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	if err := st.Servers.Upsert(ctx, servers.Config{Spec: testSpec(), Enabled: true}); err != nil {
		t.Fatal(err)
	}
	srv, _ := st.Servers.GetByKey(ctx, "fake")
	if err := st.Servers.SaveProbe(ctx, srv.Spec.ID, probe("alpha")); err != nil {
		t.Fatal(err)
	}
	if err := st.Servers.SaveProbe(ctx, srv.Spec.ID, probe()); err != nil {
		t.Fatal(err)
	}

	if rows, _ := st.Servers.ListToolsByServer(ctx, srv.Spec.ID); len(rows) != 0 {
		t.Fatalf("空清单应清空缓存，实际 %v", rows)
	}
}
