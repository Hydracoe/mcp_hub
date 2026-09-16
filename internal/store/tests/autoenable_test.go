package tests

import (
	"context"
	"strings"
	"testing"

	"github.com/hydra/mcp-hub/internal/store/servers"
	"github.com/hydra/mcp-hub/internal/store/toolchanges"
)

// O5：开了自动启用的 server，探活新发现的工具直接放行。
//
// 与安全默认（T1）是同一枚硬币的两面：默认关是"上游加的工具不许裸奔"，
// 打开是"这个上游我信，别让我一个个点"。两条路径都必须有记录可查——
// 否则事后回答不了"这个工具是谁批准上线的"。
func TestAutoEnableNewTools(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	if err := st.Servers.Upsert(ctx, servers.Config{
		Spec: testSpec(), Enabled: true, AutoEnable: true,
	}); err != nil {
		t.Fatal(err)
	}
	srv, _ := st.Servers.GetByKey(ctx, "fake")
	if !srv.AutoEnable {
		t.Fatal("回读 auto_enable 丢失：开关没落库")
	}

	stats, err := st.Servers.SaveProbeReported(ctx, srv.Spec.ID, probe("alpha", "beta"))
	if err != nil {
		t.Fatal(err)
	}
	if stats.Added != 2 || stats.AutoEnabled != 2 {
		t.Fatalf("变更统计 = added %d / autoEnabled %d, want 2/2", stats.Added, stats.AutoEnabled)
	}

	tools, err := st.Servers.ListToolsByServer(ctx, srv.Spec.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range tools {
		if !tl.Enabled {
			t.Errorf("开了自动启用，新工具 %s 仍为禁用", tl.Name)
		}
	}

	// 变更记录必须写明"按自动启用策略放行"，而不是复用引导名单那句——
	// 两句混在一起，事后就分不清是创建向导勾的还是策略放行的。
	items, _, err := st.ToolChanges.List(ctx, toolchanges.Query{ServerID: srv.Spec.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("变更记录数 = %d, want 2", len(items))
	}
	for _, c := range items {
		if !strings.Contains(c.Detail, "自动启用") {
			t.Errorf("变更明细未说明自动启用: %q", c.Detail)
		}
	}
}

// 自动启用只管**新发现**的工具：人工禁用过的工具，不能因为下一次探活
// 又活过来——"禁用"是决定，探活只是同步上游的事实。
func TestAutoEnableDoesNotResurrectDisabledTool(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	if err := st.Servers.Upsert(ctx, servers.Config{
		Spec: testSpec(), Enabled: true, AutoEnable: true,
	}); err != nil {
		t.Fatal(err)
	}
	srv, _ := st.Servers.GetByKey(ctx, "fake")
	if err := st.Servers.SaveProbe(ctx, srv.Spec.ID, probe("alpha")); err != nil {
		t.Fatal(err)
	}

	// 人工关掉它，再让上游把它报回来。
	if _, err := st.Servers.SetToolEnabled(ctx, srv.Spec.ID, "alpha", false); err != nil {
		t.Fatal(err)
	}
	stats, err := st.Servers.SaveProbeReported(ctx, srv.Spec.ID, probe("alpha", "beta"))
	if err != nil {
		t.Fatal(err)
	}
	if stats.AutoEnabled != 1 {
		t.Errorf("自动启用数 = %d, want 1（只有 beta 是新工具）", stats.AutoEnabled)
	}
	if toolByName(t, st.Servers, ctx, srv.Spec.ID, "alpha").Enabled {
		t.Error("人工禁用的 alpha 被探活复活了")
	}
	if !toolByName(t, st.Servers, ctx, srv.Spec.ID, "beta").Enabled {
		t.Error("新工具 beta 应被自动启用")
	}
}

// 默认值必须是关：存量 server 与不带这个字段的创建请求都不该被悄悄改成
// "上游加的工具自动放行"。
func TestAutoEnableDefaultsOff(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	if err := st.Servers.Upsert(ctx, servers.Config{Spec: testSpec(), Enabled: true}); err != nil {
		t.Fatal(err)
	}
	srv, err := st.Servers.GetByKey(ctx, "fake")
	if err != nil {
		t.Fatal(err)
	}
	if srv.AutoEnable {
		t.Fatal("未指定时 auto_enable 应为 false")
	}
	if err := st.Servers.SaveProbe(ctx, srv.Spec.ID, probe("alpha")); err != nil {
		t.Fatal(err)
	}
	if toolByName(t, st.Servers, ctx, srv.Spec.ID, "alpha").Enabled {
		t.Error("默认策略下新工具不该被自动启用")
	}
}
