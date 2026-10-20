package tests

import (
	"context"
	"testing"

	"github.com/hydra/mcp-hub/internal/downstream"
	"github.com/hydra/mcp-hub/internal/store/servers"
)

// gateSawToken 报告下游有没有见过某把密钥。
//
// 用"见过没有"而不是"用了几次"：握手与工具列举也在下游那边记一遍，
// 次数会随协议实现变；"有没有被选中"才是这里要断言的东西。
func gateSawToken(gate *keyedGate, token string) bool {
	for _, got := range gate.Credentials() {
		if got == "Bearer "+token {
			return true
		}
	}
	return false
}

// 停用的 server 不该继续持有可用密钥。
//
// 停用原本只做了两件事：把工具从聚合列表里摘掉、让连接退役。密钥池却留在
// 池子里——那些密钥还在，随时可能被选中发出去，停用因此只是"看起来停了"。
//
// 这里从外面观察：下游到底收到了哪把密钥。
func TestDisabledServerDropsKeyPool(t *testing.T) {
	gate := newKeyedGate("")
	url := startKeyedDownstream(t, gate, echoTool("ping"))

	spec := authSpec("pool", url, "bootstrap")
	e := newEnv(t, []downstream.Spec{spec})
	creds := addCredentials(t, e, "pool", "k1")
	serverID := e.serverIDOf(t, "pool")
	ctx := context.Background()

	// 前提：密钥池已装载，调用用的正是池里的那把密钥。
	if st := e.svc.Pool().KeyStatusesOf(serverID); len(st) != 1 {
		t.Fatalf("密钥池未装载: %+v", st)
	}
	gate.reset()
	if res := e.callTool(t, "pool__ping", `{}`); res.IsError {
		t.Fatalf("调用失败: %s", textOf(t, res))
	}
	if !gateSawToken(gate, "k1") {
		t.Fatalf("启用状态下用的不是池里的密钥: %v", gate.Credentials())
	}

	// 停用。
	if err := e.svc.SetEnabled(ctx, serverID, false); err != nil {
		t.Fatalf("停用失败: %v", err)
	}

	// 池里不该再有这把密钥。
	if st := e.svc.Pool().KeyStatusesOf(serverID); len(st) != 0 {
		t.Errorf("停用的 server 的密钥仍留在池中: %+v", st)
	}

	// 新调用不会再选中它：即便还有路径调到这台 server（例如刚停用、
	// 聚合快照还没换的那一瞬），用的也不会是池里那把密钥。
	gate.reset()
	if _, err := e.svc.Pool().CallTool(ctx, specOf(t, e, "pool"), "ping", nil); err != nil {
		t.Fatalf("调用失败: %v", err)
	}
	if gateSawToken(gate, "k1") {
		t.Errorf("停用之后仍然选中了它的密钥: %v", gate.Credentials())
	}

	// 密钥本身还在库里：停用不该动凭据，重新启用时还得用。
	saved, err := e.store.Credentials.Get(ctx, creds[0].ID)
	if err != nil {
		t.Fatalf("回读密钥失败: %v", err)
	}
	if !saved.Enabled {
		t.Error("停用 server 不该停用密钥本身：密钥要留着，重新启用时还得用")
	}
}

// 停用 → 重新启用：密钥池装回来，密钥重新参与轮换。
//
// 停用清掉的是内存里的条目，不是库里的凭据。要是重新启用后密钥装不回来，
// "停用一会儿再开"就等于把密钥全丢了。
func TestReEnabledServerRestoresKeyPool(t *testing.T) {
	gate := newKeyedGate("")
	url := startKeyedDownstream(t, gate, echoTool("ping"))

	spec := authSpec("pool", url, "bootstrap")
	e := newEnv(t, []downstream.Spec{spec})
	addCredentials(t, e, "pool", "k1")
	serverID := e.serverIDOf(t, "pool")
	ctx := context.Background()

	if err := e.svc.SetEnabled(ctx, serverID, false); err != nil {
		t.Fatalf("停用失败: %v", err)
	}
	if st := e.svc.Pool().KeyStatusesOf(serverID); len(st) != 0 {
		t.Fatalf("停用后密钥仍在池中: %+v", st)
	}

	if err := e.svc.SetEnabled(ctx, serverID, true); err != nil {
		t.Fatalf("重新启用失败: %v", err)
	}
	st := e.svc.Pool().KeyStatusesOf(serverID)
	if len(st) != 1 {
		t.Fatalf("重新启用后密钥池没有装回来: %+v", st)
	}
	if st[0].State != "active" {
		t.Errorf("重新启用后密钥状态 = %s, want active", st[0].State)
	}

	// 装回来的密钥重新参与轮换：下游看到的是它，不是配置里的静态头。
	gate.reset()
	if _, err := e.svc.Pool().CallTool(ctx, specOf(t, e, "pool"), "ping", nil); err != nil {
		t.Fatalf("调用失败: %v", err)
	}
	if !gateSawToken(gate, "k1") {
		t.Errorf("重新启用后密钥没有被选中: %v", gate.Credentials())
	}
}

// 编辑表单把 server 关掉时，密钥池一样要退场。
//
// 停用有两条路：开关（SetEnabled）与编辑表单（ApplyServer）。两条路对
// "停用"的定义必须一致——否则被编辑关掉的 server，它的密钥还留在池子里。
func TestApplyServerDisableDropsKeyPool(t *testing.T) {
	gate := newKeyedGate("")
	url := startKeyedDownstream(t, gate, echoTool("ping"))

	spec := authSpec("pool", url, "bootstrap")
	e := newEnv(t, []downstream.Spec{spec})
	addCredentials(t, e, "pool", "k1")
	serverID := e.serverIDOf(t, "pool")
	ctx := context.Background()

	if st := e.svc.Pool().KeyStatusesOf(serverID); len(st) != 1 {
		t.Fatalf("密钥池未装载: %+v", st)
	}

	// 编辑路径停用（分类沿用原样，免得被校验拦下）。
	sv, err := e.store.Servers.Get(ctx, serverID)
	if err != nil {
		t.Fatalf("读取 server 失败: %v", err)
	}
	if _, err := e.svc.ApplyServer(ctx, servers.Config{
		Spec:       sv.Spec,
		Enabled:    false,
		CategoryID: sv.CategoryID,
	}); err != nil {
		t.Fatalf("编辑停用失败: %v", err)
	}

	if st := e.svc.Pool().KeyStatusesOf(serverID); len(st) != 0 {
		t.Errorf("编辑停用后密钥仍留在池中: %+v", st)
	}
	gate.reset()
	if _, err := e.svc.Pool().CallTool(ctx, specOf(t, e, "pool"), "ping", nil); err != nil {
		t.Fatalf("调用失败: %v", err)
	}
	if gateSawToken(gate, "k1") {
		t.Errorf("编辑停用之后仍然选中了它的密钥: %v", gate.Credentials())
	}
}
