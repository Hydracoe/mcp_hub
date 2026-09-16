package tests

import (
	"context"
	"errors"
	"testing"

	"github.com/hydra/mcp-hub/internal/store/servers"
	"github.com/hydra/mcp-hub/internal/store/storeutil"
	"github.com/hydra/mcp-hub/internal/store/toolchanges"
)

// 端到端钉住产品语义：探活新发现的工具默认不进聚合端点（AI 看不到、
// 调不了），管理员逐个启用后才出现；停用立即撤下。每一步都留痕。
func TestNewToolDisabledUntilEnabled(t *testing.T) {
	ds := startDownstream(t, taggedTool("ping", "pong"))
	// 用 nil specs 起环境：避开 newEnv 的"存量已启用"模拟，
	// 保持新工具默认禁用的原始语义。
	e := newEnv(t, nil)
	ctx := context.Background()

	spec := remoteSpec("dk", ds)
	if err := e.store.Servers.Upsert(ctx, servers.Config{Spec: spec, Enabled: true}); err != nil {
		t.Fatalf("Upsert 失败: %v", err)
	}
	srv, err := e.store.Servers.GetByKey(ctx, "dk")
	if err != nil {
		t.Fatal(err)
	}
	e.svc.RefreshAll(ctx)

	// 默认禁用：聚合端点上一个工具都没有。
	if got := toolNames(e.listTools(t)); len(got) != 0 {
		t.Fatalf("默认禁用时聚合端点应为空，实际 %v", got)
	}
	// 但缓存行已经在库里，等着管理员逐个开启。
	rows, err := e.store.Servers.ListToolsByServer(ctx, srv.Spec.ID)
	if err != nil || len(rows) != 1 || rows[0].Enabled {
		t.Fatalf("缓存行状态异常: %+v err=%v", rows, err)
	}

	// 人工启用：立刻出现在 AI 的 tools/list 里。
	if err := e.svc.SetToolEnabled(ctx, srv.Spec.ID, "ping", true); err != nil {
		t.Fatalf("启用失败: %v", err)
	}
	if got := toolNames(e.listTools(t)); len(got) != 1 || got[0] != "dk__ping" {
		t.Fatalf("启用后聚合端点 = %v, want [dk__ping]", got)
	}

	// 留痕：探活的 added 之外，多一条 actor=admin 的 enabled。
	items, total, err := e.svc.ListToolChanges(ctx, toolchanges.Query{ServerID: srv.Spec.ID})
	if err != nil || total != 2 {
		t.Fatalf("变更记录异常: total=%d err=%v", total, err)
	}
	// 倒序返回，最新动作在最前。
	if items[0].Action != toolchanges.ActionEnabled || items[0].Actor != toolchanges.ActorAdmin {
		t.Errorf("启用记录异常: %+v", items[0])
	}
	if items[0].ServerKey != "dk" {
		t.Errorf("记录应带 serverKey，实际 %+v", items[0])
	}

	// 人工禁用：立刻从聚合端点撤下，不留"列表里没有但还能调"的缝。
	if err := e.svc.SetToolEnabled(ctx, srv.Spec.ID, "ping", false); err != nil {
		t.Fatalf("禁用失败: %v", err)
	}
	if got := toolNames(e.listTools(t)); len(got) != 0 {
		t.Fatalf("禁用后聚合端点应为空，实际 %v", got)
	}

	// 未知工具：404 语义，而不是静默成功。
	err = e.svc.SetToolEnabled(ctx, srv.Spec.ID, "ghost", true)
	if !errors.Is(err, storeutil.ErrNotFound) {
		t.Errorf("未知工具应返回 ErrNotFound，实际 %v", err)
	}
}

// 探活对账走到 Service 层同样成立：上游撤掉的工具被删除，并写 removed 记录。
func TestProbeRemovesTool(t *testing.T) {
	ds1 := startDownstream(t, taggedTool("alpha", "a"), taggedTool("beta", "b"))
	ds2 := startDownstream(t, taggedTool("alpha", "a"))

	e := newEnv(t, nil)
	ctx := context.Background()

	spec := remoteSpec("dk", ds1)
	if err := e.store.Servers.Upsert(ctx, servers.Config{Spec: spec, Enabled: true}); err != nil {
		t.Fatalf("Upsert 失败: %v", err)
	}
	srv, _ := e.store.Servers.GetByKey(ctx, "dk")
	e.svc.RefreshAll(ctx)

	// 上游换了：beta 被撤掉。指向新地址并刷新工具。
	newSpec := spec
	newSpec.URL = ds2
	if err := e.store.Servers.Upsert(ctx, servers.Config{Spec: newSpec, Enabled: true}); err != nil {
		t.Fatalf("更新配置失败: %v", err)
	}
	if _, err := e.svc.RefreshTools(ctx, srv.Spec.ID); err != nil {
		t.Fatalf("RefreshTools 失败: %v", err)
	}

	rows, _ := e.store.Servers.ListToolsByServer(ctx, srv.Spec.ID)
	if len(rows) != 1 || rows[0].Name != "alpha" {
		t.Fatalf("beta 应随探活删除，实际 %v", rows)
	}

	items, _, err := e.svc.ListToolChanges(ctx, toolchanges.Query{ServerID: srv.Spec.ID, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range items {
		if c.ToolName == "beta" && c.Action == toolchanges.ActionRemoved {
			found = true
		}
	}
	if !found {
		t.Errorf("缺少 beta 的 removed 记录: %+v", items)
	}
}
