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

// 凭据删除与启停在密钥池同步失败时的 HTTP 行为。
// 落库成功即算成功：密钥池属于运行时状态，同步失败只记日志，等下一次 Reload 补装。

// newEnvWithKeyRingReload 起一套网关，并把单个 server 的密钥池同步替换为 fn。
// fn 为 nil 时使用 reloadKeyRing 本体。
func newEnvWithKeyRingReload(t *testing.T, fn func(context.Context, string) error) *env {
	t.Helper()

	st, err := store.Open(storetest.DSN(t))
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	svc := hub.NewServiceWithOptions(st, zap.NewNop(), hub.Options{
		Cache:           cachetest.New(),
		ReloadKeyRingFn: fn,
	})
	t.Cleanup(func() { _ = svc.Close() })

	ts := httptest.NewServer(httpd.New(svc, httpd.Options{Logger: zap.NewNop()}).Handler())
	t.Cleanup(ts.Close)

	return &env{t: t, svc: svc, store: st, ts: ts}
}

// 同步失败时，删除与启停都必须报成功（204 / 200），不能报 500。
func TestCredentialWritesSucceedWhenKeyRingReloadFails(t *testing.T) {
	e := newEnvWithKeyRingReload(t, func(context.Context, string) error {
		return errors.New("模拟密钥池同步失败")
	})
	ds := startDownstream(t, "one")

	e.mustJSON(http.MethodPost, "/api/servers", map[string]any{
		"key":        "pool",
		"transport":  "streamable_http",
		"url":        ds,
		"categoryId": e.mustCategory(),
	}, http.StatusCreated, nil)
	sid := e.firstServerID()

	var created credentialResp
	e.mustJSON(http.MethodPost, "/api/servers/"+sid+"/credentials", map[string]any{
		"label": "主号",
		"value": "tok",
	}, http.StatusCreated, &created)

	resp, raw := e.call(http.MethodPost, "/api/servers/"+sid+"/credentials/"+created.ID+"/disable", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("停用 = %d, 期望 200\n响应: %s", resp.StatusCode, raw)
	}
	saved, err := e.store.Credentials.Get(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("回读密钥失败: %v", err)
	}
	if saved.Enabled {
		t.Error("库里应已停用")
	}

	resp, raw = e.call(http.MethodPost, "/api/servers/"+sid+"/credentials/"+created.ID+"/enable", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("启用 = %d, 期望 200\n响应: %s", resp.StatusCode, raw)
	}
	saved, err = e.store.Credentials.Get(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("回读密钥失败: %v", err)
	}
	if !saved.Enabled {
		t.Error("库里应已启用")
	}

	resp, raw = e.call(http.MethodDelete, "/api/servers/"+sid+"/credentials/"+created.ID, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("删除 = %d, 期望 204\n响应: %s", resp.StatusCode, raw)
	}
	if _, err := e.store.Credentials.Get(context.Background(), created.ID); err == nil {
		t.Error("库里还留着已删除的密钥")
	}
}
