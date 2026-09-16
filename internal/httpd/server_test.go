package httpd_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hydra/mcp-hub/internal/cache/cachetest"
	"github.com/hydra/mcp-hub/internal/httpd"
	"github.com/hydra/mcp-hub/internal/hub"
	"github.com/hydra/mcp-hub/internal/store"
	"github.com/hydra/mcp-hub/internal/store/storetest"
	"go.uber.org/zap"
)

// 这一组用例锁的是**路由层的行为契约**，与业务无关。
//
// 迁移到 gin 之后，路径匹配、尾斜杠、方法不匹配、CORS 预检这几处的
// 默认行为都与标准库 mux 不同，而且差异只以状态码的形式表现——
// 不显式钉住，就会在客户端侧变成"某个请求偶尔 301"这种最难查的形态。
// internal/api 的测试打的是业务语义，覆盖不到这些。

// newTestServer 起一个真实的服务实例：真 PostgreSQL、真 hub，只是不占端口。
// 不配置任何下游——路由行为不需要下游参与。
func newTestServer(t *testing.T, opts httpd.Options) *httptest.Server {
	t.Helper()

	st, err := store.Open(storetest.DSN(t))
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	svc := hub.NewServiceWithCache(st, nil, cachetest.New())
	t.Cleanup(func() { _ = svc.Close() })

	if opts.Logger == nil {
		opts.Logger = zap.NewNop()
	}
	opts.Addr = ""

	ts := httptest.NewServer(httpd.New(svc, opts).Handler())
	t.Cleanup(ts.Close)
	return ts
}

func do(t *testing.T, method, url string, hdr map[string]string) (*http.Response, []byte) {
	t.Helper()

	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s 失败: %v", method, url, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读取响应体失败: %v", err)
	}
	return resp, raw
}

func TestRoutingContract(t *testing.T) {
	ts := newTestServer(t, httpd.Options{})

	cases := []struct {
		name       string
		method     string
		path       string
		wantStatus int
	}{
		{"存活探针", http.MethodGet, "/healthz", http.StatusOK},
		// 一个下游都没配 → 没有工具可提供 → 未就绪。
		// 判据是"有没有工具"而不是"下游是否都健康"，见 Ready 的实现。
		{"无工具即未就绪", http.MethodGet, "/readyz", http.StatusServiceUnavailable},
		{"聚合状态", http.MethodGet, "/api/status", http.StatusOK},
		{"管理端点", http.MethodGet, "/api/servers", http.StatusOK},

		// 以下三条是 gin 与标准库 mux 的默认行为差异所在。
		{"尾斜杠不重定向", http.MethodGet, "/api/servers/", http.StatusNotFound},
		{"方法不匹配回 405", http.MethodPatch, "/api/servers", http.StatusMethodNotAllowed},
		{"近似路径不纠错", http.MethodGet, "/api/server", http.StatusNotFound},
		{"未知路径回 404", http.MethodGet, "/definitely-not-here", http.StatusNotFound},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, raw := do(t, tc.method, ts.URL+tc.path, nil)
			if resp.StatusCode != tc.wantStatus {
				t.Errorf("%s %s = %d, 期望 %d（响应: %s）",
					tc.method, tc.path, resp.StatusCode, tc.wantStatus, strings.TrimSpace(string(raw)))
			}
		})
	}
}

// 405 必须带上 Allow，否则客户端只知道"不行"，不知道"该用什么"。
func TestMethodNotAllowedAdvertisesAllow(t *testing.T) {
	ts := newTestServer(t, httpd.Options{})

	resp, _ := do(t, http.MethodPatch, ts.URL+"/api/servers", nil)
	allow := resp.Header.Get("Allow")
	if allow == "" {
		t.Fatal("405 响应缺少 Allow 头")
	}
	if !strings.Contains(allow, http.MethodGet) {
		t.Errorf("Allow = %q，期望包含 GET", allow)
	}
}

// 预检要在路由匹配之前结束：OPTIONS 若落到业务处理器上，
// 无鉴权时能通过、有鉴权时会返回 401，浏览器就直接把真实请求掐了。
func TestCORSPreflight(t *testing.T) {
	ts := newTestServer(t, httpd.Options{APIKey: "s3cret"})

	resp, raw := do(t, http.MethodOptions, ts.URL+"/api/servers", map[string]string{
		"Origin":                        "http://localhost:5173",
		"Access-Control-Request-Method": "PUT",
	})

	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("预检 = %d, 期望 204", resp.StatusCode)
	}
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "http://localhost:5173" {
		t.Errorf("Allow-Origin = %q, 期望回显来源", got)
	}
	if len(raw) != 0 {
		t.Errorf("预检不该有响应体，实际: %s", raw)
	}

	// 普通请求也要带上 CORS 头，否则浏览器拿不到响应内容。
	resp, _ = do(t, http.MethodGet, ts.URL+"/healthz", map[string]string{
		"Origin": "http://localhost:5173",
	})
	if resp.Header.Get("Access-Control-Allow-Origin") == "" {
		t.Error("普通请求缺少 Access-Control-Allow-Origin")
	}
}

// 鉴权作用域：/api/* 归管理 API 管，健康探针与 /api/status 刻意不设防。
//
// /api/status 是给探活与运维脚本用的，加鉴权等于让它在有 APIKey 的
// 部署里直接失效。
func TestAPIGuardScope(t *testing.T) {
	ts := newTestServer(t, httpd.Options{APIKey: "s3cret"})

	cases := []struct {
		name       string
		path       string
		hdr        map[string]string
		wantStatus int
	}{
		{"无凭据被拒", "/api/overview", nil, http.StatusUnauthorized},
		{"错误凭据被拒", "/api/overview", map[string]string{"X-API-Key": "wrong"}, http.StatusUnauthorized},
		{"X-API-Key 放行", "/api/overview", map[string]string{"X-API-Key": "s3cret"}, http.StatusOK},
		{"Bearer 放行", "/api/overview", map[string]string{"Authorization": "Bearer s3cret"}, http.StatusOK},
		{"聚合状态不设防", "/api/status", nil, http.StatusOK},
		{"存活探针不设防", "/healthz", nil, http.StatusOK},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, raw := do(t, http.MethodGet, ts.URL+tc.path, tc.hdr)
			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("%s = %d, 期望 %d（响应: %s）",
					tc.path, resp.StatusCode, tc.wantStatus, strings.TrimSpace(string(raw)))
			}
			if tc.wantStatus == http.StatusUnauthorized {
				var body struct {
					Code string `json:"code"`
				}
				if err := json.Unmarshal(raw, &body); err != nil {
					t.Fatalf("401 响应体不是 JSON: %v", err)
				}
				if body.Code != "unauthorized" {
					t.Errorf("code = %q, 期望 unauthorized", body.Code)
				}
			}
		})
	}
}

// MCP 端点由 mcp-go 提供 handler，经 gin.WrapH 挂进路由树。
// 这里只确认"路由挂上了"：协议层面的行为由 api 包的端到端用例覆盖。
func TestMCPEndpointRouted(t *testing.T) {
	ts := newTestServer(t, httpd.Options{})

	for _, path := range []string{"/mcp", "/mcp/"} {
		t.Run(path, func(t *testing.T) {
			resp, _ := do(t, http.MethodPost, ts.URL+path, map[string]string{
				"Content-Type": "application/json",
				"Accept":       "application/json, text/event-stream",
			})
			// 405 也是合理结果（无状态模式下某些方法不支持），
			// 但 404 意味着路由没接上。
			if resp.StatusCode == http.StatusNotFound {
				t.Errorf("POST %s = 404，MCP 端点未接入 gin 路由树", path)
			}
		})
	}
}
