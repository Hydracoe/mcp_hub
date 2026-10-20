package tests

import (
	"context"
	"errors"
	"testing"

	"github.com/hydra/mcp-hub/internal/cache/cachetest"
	"github.com/hydra/mcp-hub/internal/hub"
	"github.com/hydra/mcp-hub/internal/store"
	"github.com/hydra/mcp-hub/internal/store/servers"
	"github.com/hydra/mcp-hub/internal/store/storetest"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// openSvcWithReload 起一个服务层，并把"配置刷新"这一步换成 reloadFn。
//
// reloadFn 为 nil 时用 Reload 本体；非 nil 时它就是刷新本身，
// 用来复现"写已落库、刷新失败"这条路径（见 Options.ReloadFn）。
func openSvcWithReload(t *testing.T, logger *zap.Logger, reloadFn func(context.Context) error) (*store.Store, *hub.Service) {
	t.Helper()

	st, err := store.Open(storetest.DSN(t))
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	svc := hub.NewServiceWithOptions(st, logger, hub.Options{
		Cache:    cachetest.New(),
		ReloadFn: reloadFn,
	})
	t.Cleanup(func() { svc.Close() })
	return st, svc
}

// failReload 是一个恒定失败的重载实现。
func failReload() func(context.Context) error {
	return func(context.Context) error { return errors.New("模拟刷新失败") }
}

// hasErrorLog 检查错误日志里有没有带指定 server key 的一条。
func hasErrorLog(logs *observer.ObservedLogs, serverKey string) bool {
	for _, e := range logs.All() {
		if e.Level != zapcore.ErrorLevel {
			continue
		}
		if e.ContextMap()["server"] == serverKey {
			return true
		}
	}
	return false
}

// 写已落库、刷新失败：写操作必须算成功，缓存必须失效，失败要留痕。
//
// 缺陷形状：Upsert 已经提交，之后 Reload 失败就把整个操作报成失败，
// 而且缓存的失效被跳过——界面看到的还是旧配置，下次读还会命中旧值。
// 正确语义是"落库成功即成功"：刷新属于运行时状态，失败只记日志。
func TestApplyServerSucceedsWhenReloadFails(t *testing.T) {
	core, logs := observer.New(zapcore.ErrorLevel)
	st, svc := openSvcWithReload(t, zap.New(core), failReload())
	ctx := context.Background()

	// 先读一次列表，把空列表缓存住：写完之后必须能看到新 server。
	if list, err := svc.Servers(ctx); err != nil || len(list) != 0 {
		t.Fatalf("初始列表 = %d 条, err=%v", len(list), err)
	}

	cat, err := svc.CreateCategory(ctx, "天气", "weather", "天气查询")
	if err != nil {
		t.Fatalf("建分类失败: %v", err)
	}

	down := startDownstream(t, toolSpec{name: "forecast", desc: "查询城市天气"})
	res, err := svc.ApplyServer(ctx, servers.Config{
		Spec:       remoteSpec("wx", down),
		Enabled:    true,
		CategoryID: cat.ID,
	})
	if err != nil {
		t.Fatalf("刷新失败不该把已落库的写判成失败: %v", err)
	}
	if res == nil || res.ID == "" {
		t.Fatalf("应返回创建结果: %+v", res)
	}

	// 配置确实写进库了。
	if _, err := st.Servers.GetByKey(ctx, "wx"); err != nil {
		t.Fatalf("配置应已落库: %v", err)
	}

	// 缓存被失效：下一次读看到的是新配置，不是旧列表。
	list, err := svc.Servers(ctx)
	if err != nil {
		t.Fatalf("读列表失败: %v", err)
	}
	if len(list) != 1 || list[0].Spec.Key != "wx" {
		t.Fatalf("缓存未失效，读到的还是旧列表: %+v", list)
	}

	// 刷新失败要留下 error 级日志，并带上定位信息。
	if !hasErrorLog(logs, "wx") {
		t.Fatalf("应留下带 server key 的 error 日志: %+v", logs.All())
	}
}

// 删除与启停同样是"落库在前、刷新在后"：刷新失败只记日志，
// 写操作照常算成功，缓存照常失效。
func TestRemoveAndSetEnabledSucceedWhenReloadFails(t *testing.T) {
	core, logs := observer.New(zapcore.ErrorLevel)
	st, svc := openSvcWithReload(t, zap.New(core), failReload())
	ctx := context.Background()

	cat, err := svc.CreateCategory(ctx, "天气", "weather", "天气查询")
	if err != nil {
		t.Fatalf("建分类失败: %v", err)
	}
	down := startDownstream(t, toolSpec{name: "forecast", desc: "查询城市天气"})
	res, err := svc.ApplyServer(ctx, servers.Config{
		Spec:       remoteSpec("wx", down),
		Enabled:    true,
		CategoryID: cat.ID,
	})
	if err != nil {
		t.Fatalf("建 server 失败: %v", err)
	}

	// 缓存住"启用中"的列表。
	list, err := svc.Servers(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("列表 = %d 条, err=%v", len(list), err)
	}

	// 停用：刷新失败不该报成失败。
	if err := svc.SetEnabled(ctx, res.ID, false); err != nil {
		t.Fatalf("停用不该因刷新失败而失败: %v", err)
	}
	sv, err := st.Servers.Get(ctx, res.ID)
	if err != nil {
		t.Fatalf("读 server 失败: %v", err)
	}
	if sv.Enabled {
		t.Error("库里应已停用")
	}

	// 缓存失效：读回来应是停用后的状态。
	list, err = svc.Servers(ctx)
	if err != nil {
		t.Fatalf("读列表失败: %v", err)
	}
	if len(list) != 1 || list[0].Enabled {
		t.Fatalf("缓存未失效，读到的还是旧状态: %+v", list)
	}

	// 删除：同样不该报成失败。
	if err := svc.RemoveServer(ctx, res.ID); err != nil {
		t.Fatalf("删除不该因刷新失败而失败: %v", err)
	}
	if _, err := st.Servers.Get(ctx, res.ID); err == nil {
		t.Error("库里应已删除")
	}
	list, err = svc.Servers(ctx)
	if err != nil {
		t.Fatalf("读列表失败: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("缓存未失效，列表里还留着已删的 server: %+v", list)
	}

	if !hasErrorLog(logs, "wx") {
		t.Fatalf("应留下带 server key 的 error 日志: %+v", logs.All())
	}
}
