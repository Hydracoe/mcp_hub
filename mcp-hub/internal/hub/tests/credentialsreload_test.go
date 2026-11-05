package tests

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hydra/mcp-hub/internal/cache/cachetest"
	"github.com/hydra/mcp-hub/internal/downstream"
	"github.com/hydra/mcp-hub/internal/hub"
	"github.com/hydra/mcp-hub/internal/store"
	"github.com/hydra/mcp-hub/internal/store/credentials"
	"github.com/hydra/mcp-hub/internal/store/servers"
	"github.com/hydra/mcp-hub/internal/store/storetest"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// 凭据删除与启停在"密钥池同步失败"时的测试。
// 正确语义是"落库成功即成功"：密钥池属于运行时状态，同步失败只记 error 日志，
// 等下一次 Reload 补装，不能把已提交的操作报成失败（HTTP 500）。

// openSvcWithKeyRingReload 起一个服务层，并把"单个 server 的密钥池同步"
// 这一步换成 fn。fn 留空用 reloadKeyRing 本体，非 nil 时用来复现
// "凭据写已落库、密钥池同步失败"这条路径（见 Options.ReloadKeyRingFn）。
func openSvcWithKeyRingReload(t *testing.T, logger *zap.Logger, fn func(context.Context, string) error) (*store.Store, *hub.Service) {
	t.Helper()

	st, err := store.Open(storetest.DSN(t))
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	svc := hub.NewServiceWithOptions(st, logger, hub.Options{
		Cache:           cachetest.New(),
		ReloadKeyRingFn: fn,
	})
	t.Cleanup(func() { svc.Close() })
	return st, svc
}

// failKeyRingReload 是一个恒定失败的密钥池同步实现。
// calls 记录调用次数：断言确实调过，否则接缝没接上时用例会空转通过。
func failKeyRingReload(calls *atomic.Int64) func(context.Context, string) error {
	return func(context.Context, string) error {
		calls.Add(1)
		return errors.New("模拟密钥池同步失败")
	}
}

// seedKeyPoolServer 写一个远程 server（密钥池只支持远程传输）并挂一把密钥，
// 返回 server id 与密钥 id。URL 指向没人监听的端口：用例只关心密钥池的装载
// 与同步，不会真的去连下游。
func seedKeyPoolServer(t *testing.T, st *store.Store, key string) (string, string) {
	t.Helper()
	ctx := context.Background()

	spec := downstream.Spec{
		ID:        "srv-" + key,
		Key:       key,
		Name:      key,
		Transport: downstream.TransportStreamableHTTP,
		URL:       "http://127.0.0.1:1/mcp",
		Timeout:   5 * time.Second,
	}
	if err := st.Servers.Upsert(ctx, servers.Config{Spec: spec, Enabled: true}); err != nil {
		t.Fatalf("写入 server %s 失败: %v", key, err)
	}
	cred, err := st.Credentials.Insert(ctx, credentials.Credential{
		ServerID: spec.ID,
		Label:    "主号",
		Headers:  map[string]string{"Authorization": "Bearer tok"},
		Enabled:  true,
	})
	if err != nil {
		t.Fatalf("写入密钥失败: %v", err)
	}
	return spec.ID, cred.ID
}

// poolKeyCount 返回某个 server 当前密钥池里的密钥数（单密钥模式下为 0）。
func poolKeyCount(svc *hub.Service, serverID string) int {
	ring := svc.Pool().KeyRing(serverID)
	if ring == nil {
		return 0
	}
	return ring.Len()
}

// poolKeyStatus 返回某把密钥在密钥池里的状态快照。
func poolKeyStatus(svc *hub.Service, serverID, credID string) (downstream.KeyStatus, bool) {
	for _, st := range svc.Pool().KeyStatusesOf(serverID) {
		if st.ID == credID {
			return st, true
		}
	}
	return downstream.KeyStatus{}, false
}

// 删除：密钥池同步失败不该把一次已经提交的删除报成失败。
func TestDeleteCredentialSucceedsWhenKeyRingReloadFails(t *testing.T) {
	core, logs := observer.New(zapcore.ErrorLevel)
	var calls atomic.Int64
	st, svc := openSvcWithKeyRingReload(t, zap.New(core), failKeyRingReload(&calls))
	ctx := context.Background()

	serverID, credID := seedKeyPoolServer(t, st, "delsrv")

	// 先让密钥池装载上这把密钥：待会儿要证明"同步失败"确实发生在
	// 删除之后，而不是删除压根没走到刷新这一步。
	if err := svc.Reload(ctx); err != nil {
		t.Fatalf("重载失败: %v", err)
	}
	if got := poolKeyCount(svc, serverID); got != 1 {
		t.Fatalf("密钥池 %d 把, 期望 1 把", got)
	}

	if err := svc.DeleteCredential(ctx, serverID, credID); err != nil {
		t.Fatalf("删除不该因密钥池同步失败而失败: %v", err)
	}

	// 库里确实没有这把密钥了：删除已经提交。
	if _, err := st.Credentials.Get(ctx, credID); err == nil {
		t.Error("库里还留着已删除的密钥")
	}

	// 同步确实被调过并失败了（否则这条用例什么都没验）。
	if calls.Load() == 0 {
		t.Fatal("密钥池同步没有被调用：用例没走到刷新那一步")
	}

	// 运行时状态没跟上——这正是"只记日志"的含义：池子里还是旧的，
	// 直到下一次 Reload 才补装。
	if got := poolKeyCount(svc, serverID); got != 1 {
		t.Errorf("同步失败后密钥池 %d 把, 期望仍是 1 把（运行时状态保持原样）", got)
	}
	if err := svc.Reload(ctx); err != nil {
		t.Fatalf("重载失败: %v", err)
	}
	if got := poolKeyCount(svc, serverID); got != 0 {
		t.Errorf("重载后密钥池 %d 把, 期望 0 把（已删除的密钥不该被补装回来）", got)
	}

	// 失败要留下 error 级日志，并带上定位信息。
	if !hasErrorLog(logs, "delsrv") {
		t.Fatalf("应留下带 server key 的 error 日志: %+v", logs.All())
	}
}

// 启停：密钥池同步失败不该把一次已经提交的启停报成失败。
func TestSetCredentialEnabledSucceedsWhenKeyRingReloadFails(t *testing.T) {
	core, logs := observer.New(zapcore.ErrorLevel)
	var calls atomic.Int64
	st, svc := openSvcWithKeyRingReload(t, zap.New(core), failKeyRingReload(&calls))
	ctx := context.Background()

	serverID, credID := seedKeyPoolServer(t, st, "togsrv")
	if err := svc.Reload(ctx); err != nil {
		t.Fatalf("重载失败: %v", err)
	}
	if got := poolKeyCount(svc, serverID); got != 1 {
		t.Fatalf("密钥池 %d 把, 期望 1 把", got)
	}

	// 停用。
	if err := svc.SetCredentialEnabled(ctx, serverID, credID, false); err != nil {
		t.Fatalf("停用不该因密钥池同步失败而失败: %v", err)
	}
	saved, err := st.Credentials.Get(ctx, credID)
	if err != nil {
		t.Fatalf("回读密钥失败: %v", err)
	}
	if saved.Enabled {
		t.Error("库里应已停用")
	}

	// 启用。
	if err := svc.SetCredentialEnabled(ctx, serverID, credID, true); err != nil {
		t.Fatalf("启用不该因密钥池同步失败而失败: %v", err)
	}
	saved, err = st.Credentials.Get(ctx, credID)
	if err != nil {
		t.Fatalf("回读密钥失败: %v", err)
	}
	if !saved.Enabled {
		t.Error("库里应已启用")
	}

	if calls.Load() == 0 {
		t.Fatal("密钥池同步没有被调用：用例没走到刷新那一步")
	}

	// 运行时状态没跟上：池子里还是启用中的那把，直到下一次 Reload。
	status, ok := poolKeyStatus(svc, serverID, credID)
	if !ok {
		t.Fatalf("密钥池里没有这把密钥: %+v", svc.Pool().KeyStatusesOf(serverID))
	}
	if !status.Enabled {
		t.Error("同步失败后密钥池里应仍是启用态（运行时状态保持原样）")
	}

	// 下一次 Reload 补装：库里是启用态，池子里也该是。
	if err := svc.Reload(ctx); err != nil {
		t.Fatalf("重载失败: %v", err)
	}
	status, ok = poolKeyStatus(svc, serverID, credID)
	if !ok {
		t.Fatalf("重载后密钥池里没有这把密钥: %+v", svc.Pool().KeyStatusesOf(serverID))
	}
	if !status.Enabled || status.State != downstream.KeyActive {
		t.Errorf("重载后密钥状态 = %+v, 期望启用且 active", status)
	}

	// 失败要留下 error 级日志，并带上定位信息。
	if !hasErrorLog(logs, "togsrv") {
		t.Fatalf("应留下带 server key 的 error 日志: %+v", logs.All())
	}
}
