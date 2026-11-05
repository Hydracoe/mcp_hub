package tests

import (
	"context"
	"errors"
	"testing"

	"github.com/hydra/mcp-hub/internal/hub"
	"github.com/hydra/mcp-hub/internal/store/servers"
	"github.com/hydra/mcp-hub/internal/store/toolchanges"
)

// cascadeEnv 起一个带两个已启用工具的 server（走引导名单，不经人工启停，
// 工具上没有 admin 记录），返回 server 行。
func cascadeEnv(t *testing.T, key string, tools ...toolSpec) (*env, servers.Server) {
	t.Helper()

	ds := startDownstream(t, tools...)
	e := newEnv(t, nil)
	ctx := context.Background()

	if err := e.store.Servers.Upsert(ctx, servers.Config{
		Spec:         remoteSpec(key, ds),
		Enabled:      true,
		EnabledTools: specNames(tools),
	}); err != nil {
		t.Fatalf("Upsert 失败: %v", err)
	}
	e.svc.RefreshAll(ctx)

	srv, err := e.store.Servers.GetByKey(ctx, key)
	if err != nil {
		t.Fatalf("读取 server 失败: %v", err)
	}
	return e, srv
}

// specNames 取工具名列表（给引导名单用）。
func specNames(tools []toolSpec) []string {
	out := make([]string, 0, len(tools))
	for _, ts := range tools {
		out = append(out, ts.name)
	}
	return out
}

// enabledToolsOf 读某 server 缓存里处于启用态的工具名集合。
func enabledToolsOf(t *testing.T, e *env, serverID string) map[string]bool {
	t.Helper()
	tools, err := e.store.Servers.ListToolsByServer(context.Background(), serverID)
	if err != nil {
		t.Fatalf("读取工具缓存失败: %v", err)
	}
	out := make(map[string]bool, len(tools))
	for _, tl := range tools {
		if tl.Enabled {
			out[tl.Name] = true
		}
	}
	return out
}

// 约束一：停用 server 必须级联禁用它的全部工具；重新启用 server 不恢复
// 工具——没有 admin 记录的工具回到「待启用」，等人工重新决定。
func TestDisableServerCascadesToTools(t *testing.T) {
	e, srv := cascadeEnv(t, "casc", taggedTool("alpha", "a"), taggedTool("beta", "b"))
	ctx := context.Background()

	if got := enabledToolsOf(t, e, srv.Spec.ID); len(got) != 2 {
		t.Fatalf("前置条件：应有 2 个启用工具，实际 %v", got)
	}

	if err := e.svc.SetEnabled(ctx, srv.Spec.ID, false); err != nil {
		t.Fatalf("停用失败: %v", err)
	}
	if got := enabledToolsOf(t, e, srv.Spec.ID); len(got) != 0 {
		t.Errorf("停用后仍有启用工具: %v", got)
	}

	// 级联要在变更记录里留痕，且操作者是 system：管理员动的是 server 开关，
	// 没有对每个工具表态。
	changes, _, err := e.store.ToolChanges.List(ctx, toolchanges.Query{ServerID: srv.Spec.ID, Limit: 50})
	if err != nil {
		t.Fatalf("读取变更记录失败: %v", err)
	}
	cascaded := make(map[string]bool)
	for _, ch := range changes {
		if ch.Action == toolchanges.ActionDisabled && ch.Actor == toolchanges.ActorSystem {
			cascaded[ch.ToolName] = true
		}
	}
	if !cascaded["alpha"] || !cascaded["beta"] {
		t.Errorf("级联禁用未留痕: %v", cascaded)
	}

	// 重新启用 server：工具保持禁用，等人工重新决定（待启用告警重新出现）。
	if err := e.svc.SetEnabled(ctx, srv.Spec.ID, true); err != nil {
		t.Fatalf("重新启用失败: %v", err)
	}
	if got := enabledToolsOf(t, e, srv.Spec.ID); len(got) != 0 {
		t.Errorf("重新启用后工具应保持禁用，实际: %v", got)
	}
	if n, err := e.svc.PendingEnableCount(ctx); err != nil || n != 2 {
		t.Errorf("PendingEnableCount = %d, %v; want 2", n, err)
	}
}

// 约束二：启用工具要求所属 server 处于启用状态。
func TestEnableToolRequiresServerEnabled(t *testing.T) {
	e, srv := cascadeEnv(t, "gate", taggedTool("solo", "s"))
	ctx := context.Background()

	if err := e.svc.SetEnabled(ctx, srv.Spec.ID, false); err != nil {
		t.Fatalf("停用失败: %v", err)
	}

	err := e.svc.SetToolEnabled(ctx, srv.Spec.ID, "solo", true)
	if !errors.Is(err, hub.ErrServerDisabled) {
		t.Fatalf("停用 server 下启用工具应返回 ErrServerDisabled，得到 %v", err)
	}

	if err := e.svc.SetEnabled(ctx, srv.Spec.ID, true); err != nil {
		t.Fatalf("重新启用失败: %v", err)
	}
	if err := e.svc.SetToolEnabled(ctx, srv.Spec.ID, "solo", true); err != nil {
		t.Errorf("启用 server 后启用工具不应报错: %v", err)
	}
}

// 编辑配置把 server 关掉（PUT 路径）与独立启停开关服从同一约束。
func TestUpsertDisabledServerCascadesToTools(t *testing.T) {
	e, srv := cascadeEnv(t, "edit", taggedTool("one", "1"))
	ctx := context.Background()

	// 编辑为停用：spec 原样带回，只翻 Enabled。
	spec := srv.Spec
	if _, err := e.svc.ApplyServer(ctx, servers.Config{Spec: spec, Enabled: false}); err != nil {
		t.Fatalf("ApplyServer 失败: %v", err)
	}
	if got := enabledToolsOf(t, e, srv.Spec.ID); len(got) != 0 {
		t.Errorf("编辑停用后仍有启用工具: %v", got)
	}
}
