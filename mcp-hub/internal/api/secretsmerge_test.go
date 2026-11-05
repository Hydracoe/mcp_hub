package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/hydra/mcp-hub/internal/httpd"
)

// 这组用例钉住 headers / env 的三态语义：
// 没带 = 沿用原值；显式给空 = 清空；非空 = 整体替换，值为 *** 的按原值还原。
// 清空必须是明写的意图，不能是“忘了带”的副作用。

// 建一个带凭据的 server，返回它的 id。
func mustServerWithSecrets(t *testing.T, e *env, key string) string {
	t.Helper()
	ds := startDownstream(t, "one")

	var created serverResp
	e.mustJSON(http.MethodPost, "/api/servers", map[string]any{
		"key":        key,
		"transport":  "streamable_http",
		"url":        ds,
		"headers":    map[string]string{"Authorization": "Bearer secret", "X-Tenant": "t1"},
		"env":        []string{"API_KEY=env-secret"},
		"categoryId": e.mustCategory(),
	}, http.StatusCreated, &created)
	return created.ID
}

// 编辑请求不带 headers / env：已有密钥必须原样留下。
// “没提”不等于“清空”，请求体没带的字段应沿用旧值。
func TestUpdateServerKeepsHeadersAndEnvWhenOmitted(t *testing.T) {
	e := newEnv(t, httpd.Options{})
	id := mustServerWithSecrets(t, e, "keep")
	ds := startDownstream(t, "one")

	e.mustJSON(http.MethodPut, "/api/servers/"+id, map[string]any{
		"key":       "keep",
		"name":      "只改了个名字",
		"transport": "streamable_http",
		"url":       ds,
	}, http.StatusOK, nil)

	saved, err := e.store.Servers.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("读 server 失败: %v", err)
	}
	if got := saved.Spec.Headers["Authorization"]; got != "Bearer secret" {
		t.Errorf("没带 headers 时凭据被抹掉了: %q", got)
	}
	if got := saved.Spec.Headers["X-Tenant"]; got != "t1" {
		t.Errorf("没带 headers 时附加头被抹掉了: %q", got)
	}
	if got := strings.Join(saved.Spec.Env, ","); !strings.Contains(got, "API_KEY=env-secret") {
		t.Errorf("没带 env 时环境变量被抹掉了: %q", got)
	}
}

// 显式给了空 map / 空数组才是"清空"：与"请求体没带这个字段"是两回事。
func TestUpdateServerExplicitlyClearsHeadersAndEnv(t *testing.T) {
	e := newEnv(t, httpd.Options{})
	id := mustServerWithSecrets(t, e, "clear")
	ds := startDownstream(t, "one")

	e.mustJSON(http.MethodPut, "/api/servers/"+id, map[string]any{
		"key":       "clear",
		"transport": "streamable_http",
		"url":       ds,
		"headers":   map[string]string{},
		"env":       []string{},
	}, http.StatusOK, nil)

	saved, err := e.store.Servers.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("读 server 失败: %v", err)
	}
	if len(saved.Spec.Headers) != 0 {
		t.Errorf("显式清空后 headers = %v, 期望为空", saved.Spec.Headers)
	}
	if len(saved.Spec.Env) != 0 {
		t.Errorf("显式清空后 env = %v, 期望为空", saved.Spec.Env)
	}
}

// 给了非空内容就按整体替换处理：值为 *** 的按原值还原，新键写入，
// 没提到的旧键被替换掉（server 配置本来就是整体替换的语义）。
func TestUpdateServerReplacesHeadersKeepingMaskedValues(t *testing.T) {
	e := newEnv(t, httpd.Options{})
	id := mustServerWithSecrets(t, e, "replace")
	ds := startDownstream(t, "one")

	e.mustJSON(http.MethodPut, "/api/servers/"+id, map[string]any{
		"key":       "replace",
		"transport": "streamable_http",
		"url":       ds,
		"headers":   map[string]string{"Authorization": "***", "X-Extra": "v"},
		"env":       []string{"API_KEY=***"},
	}, http.StatusOK, nil)

	saved, err := e.store.Servers.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("读 server 失败: %v", err)
	}
	if got := saved.Spec.Headers["Authorization"]; got != "Bearer secret" {
		t.Errorf("掩码值没有按原值还原: %q", got)
	}
	if got := saved.Spec.Headers["X-Extra"]; got != "v" {
		t.Errorf("新键没有被写入: %q", got)
	}
	if _, ok := saved.Spec.Headers["X-Tenant"]; ok {
		t.Errorf("整体替换语义下未提及的旧键应当被替换掉: %v", saved.Spec.Headers)
	}
	if got := strings.Join(saved.Spec.Env, ","); !strings.Contains(got, "API_KEY=env-secret") {
		t.Errorf("env 的掩码值没有按原值还原: %q", got)
	}
}
