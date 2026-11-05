package api_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hydra/mcp-hub/internal/cache/cachetest"
	"github.com/hydra/mcp-hub/internal/httpd"
	"github.com/hydra/mcp-hub/internal/hub"
	"github.com/hydra/mcp-hub/internal/store"
	"github.com/hydra/mcp-hub/internal/store/storetest"
	"go.uber.org/zap"
)

// newEnvWithReload 起一套网关，并把"配置刷新"这一步换成 reloadFn。
//
// reloadFn 为 nil 时用 Reload 本体；非 nil 时它就是刷新本身，
// 用来复现"写已落库、刷新失败"这条路径（见 hub.Options.ReloadFn）。
func newEnvWithReload(t *testing.T, reloadFn func(context.Context) error) *env {
	t.Helper()

	st, err := store.Open(storetest.DSN(t))
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	svc := hub.NewServiceWithOptions(st, zap.NewNop(), hub.Options{
		Cache:    cachetest.New(),
		ReloadFn: reloadFn,
	})
	t.Cleanup(func() { _ = svc.Close() })

	ts := httptest.NewServer(httpd.New(svc, httpd.Options{Logger: zap.NewNop()}).Handler())
	t.Cleanup(ts.Close)

	return &env{t: t, svc: svc, store: st, ts: ts}
}

// 写已落库、刷新失败：管理接口必须报成功，不能报 500。
//
// 缺陷形状：Upsert / SetEnabledCascade / Delete 已经提交，之后 Reload
// 失败就把整个操作报成失败（500），而且缓存的失效被跳过——列表接口
// 读到的还是旧配置。正确语义是"落库成功即成功"：刷新属于运行时状态。
func TestServerWritesSucceedWhenReloadFails(t *testing.T) {
	e := newEnvWithReload(t, func(context.Context) error {
		return errors.New("模拟刷新失败")
	})

	// 先读一次列表，把空列表缓存住。
	var list serverListResp
	e.get("/api/servers", &list)
	if list.Total != 0 {
		t.Fatalf("初始列表 = %d 条, 期望 0", list.Total)
	}

	ds := startDownstream(t, "alpha")
	var created serverResp
	e.mustJSON(http.MethodPost, "/api/servers", map[string]any{
		"key":        "ds",
		"transport":  "streamable_http",
		"url":        ds,
		"timeoutMs":  5000,
		"categoryId": e.mustCategory(),
	}, http.StatusCreated, &created)
	if created.ID == "" {
		t.Fatal("创建后没有返回 id")
	}

	// 缓存必须已失效：列表里能看到刚建的 server。
	e.get("/api/servers", &list)
	if list.Total != 1 || list.Items[0].Key != "ds" {
		t.Fatalf("缓存未失效，列表 = %+v", list)
	}

	// 停用：同样不能报 500。
	var disabled serverResp
	e.mustJSON(http.MethodPost, "/api/servers/"+created.ID+"/disable", nil, http.StatusOK, &disabled)
	e.get("/api/servers", &list)
	if list.Total != 1 || list.Items[0].Enabled {
		t.Fatalf("停用后列表 = %+v", list)
	}

	// 删除：同样不能报 500。
	resp, _ := e.call(http.MethodDelete, "/api/servers/"+created.ID, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("删除 = %d, 期望 204", resp.StatusCode)
	}
	e.get("/api/servers", &list)
	if list.Total != 0 {
		t.Fatalf("删除后列表 = %d 条, 期望 0", list.Total)
	}
}
