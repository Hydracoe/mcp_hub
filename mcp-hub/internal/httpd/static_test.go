package httpd_test

import (
	"io/fs"
	"net/http"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/hydra/mcp-hub/internal/httpd"
)

// 这一组用例锁的是"管理界面挂上去之后，路由层没有被打乱"。
//
// 内嵌界面的挂载点是 gin 的 NoRoute，也就是路由树的兜底分支。
// 它一旦挂错位置（比如注册成 "/" 通配或挂在中间件之后），
// /api 的响应会变成一份 HTML——而前端拿到 HTML 去 JSON.parse 时
// 抛出的错误指向完全无关的地方，是最难查的一类故障。
//
// 所以这里断言的不是"界面能打开"，而是"界面的存在不影响接口"。

// fakeUI 是一份最小可用的前端产物：入口页 + 一个带哈希的资源。
func fakeUI() fstest.MapFS {
	return fstest.MapFS{
		"index.html":             &fstest.MapFile{Data: []byte("<html>app shell</html>")},
		"assets/index-a1b2c3.js": &fstest.MapFile{Data: []byte("console.log(1)")},
	}
}

// 把 handlerFor 的等价行为搬过来：httpd 拿到的只是一个 http.Handler，
// 它不关心实现，所以这里用最小实现替身即可，避免跨包依赖 webassets——
// 这条边界本身也是被测的：httpd 不该知道界面从哪来。
func fakeUIHandler(t *testing.T) http.Handler {
	t.Helper()
	fsys := fakeUI()
	files := http.FileServer(http.FS(fsys))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.NotFound(w, r)
			return
		}
		if info, err := fs.Stat(fsys, strings.TrimPrefix(r.URL.Path, "/")); err == nil && !info.IsDir() {
			files.ServeHTTP(w, r)
			return
		}
		b, _ := fs.ReadFile(fsys, "index.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(b)
	})
}

// TestStaticDoesNotShadowAPI 是这一组的核心：挂了界面之后 /api 仍然返回 JSON。
func TestStaticDoesNotShadowAPI(t *testing.T) {
	ts := newTestServer(t, httpd.Options{Static: fakeUIHandler(t)})

	cases := []struct {
		method, path string
		wantStatus   int // 0 表示只校验 Content-Type
		wantJSON     bool
	}{
		// 命中具体路由，必须还是 JSON。
		{http.MethodGet, "/api/overview", http.StatusOK, true},
		{http.MethodGet, "/api/status", http.StatusOK, true},
		{http.MethodGet, "/healthz", http.StatusOK, true},
		// 没配任何下游 → not_ready，503 是正确响应；这里只钉住"它还是 JSON"。
		{http.MethodGet, "/readyz", 0, true},
		// /api 下不存在的路径：这是最危险的一条。NoRoute 会接住它，
		// 于是返回 index.html——但状态码必须是 404、且不能是 200。
		// 前端据此报错，而不是把 HTML 当数据。
		{http.MethodGet, "/api/does-not-exist", http.StatusNotFound, false},
		// 方法不匹配仍然 405：NoRoute 不参与方法匹配，界面不该把它吃掉。
		{http.MethodPost, "/api/overview", http.StatusMethodNotAllowed, false},
	}

	for _, tc := range cases {
		resp, body := do(t, tc.method, ts.URL+tc.path, nil)
		if tc.wantStatus != 0 && resp.StatusCode != tc.wantStatus {
			t.Errorf("%s %s: 状态码 = %d，想要 %d（body=%q）",
				tc.method, tc.path, resp.StatusCode, tc.wantStatus, body)
			continue
		}
		if tc.wantJSON {
			ct := resp.Header.Get("Content-Type")
			if !strings.Contains(ct, "json") {
				t.Errorf("%s %s: Content-Type = %q，想要 JSON", tc.method, tc.path, ct)
			}
		}
	}
}

// TestStaticFallbackIs200NotStuck404 锁住 gin NoRoute 的那个坑。
//
// gin 走 NoRoute 时先经 serveError(c, 404, ...)，已经把状态预置成 404；
// 静态处理器若只 set 头 + Write（不显式 WriteHeader），响应就会带着
// 404 发出。表现是"页面能显示内容，但浏览器/代理按 404 处理"——
// 排查时极容易被当成前端问题。这条用例直接把它钉死。
func TestStaticFallbackIs200NotStuck404(t *testing.T) {
	ts := newTestServer(t, httpd.Options{Static: fakeUIHandler(t)})

	for _, path := range []string{"/", "/overview", "/servers/abc"} {
		resp, body := do(t, http.MethodGet, ts.URL+path, nil)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s: 状态码 = %d，想要 200（body=%q）", path, resp.StatusCode, body)
		}
	}
}

// TestStaticServesUnknownPath 未命中任何路由的 GET 应落到界面。
//
// 这是桌面端与 Web 部署的"刷新不 404"依赖的那一条：
// 后端在 /overview 这类路径上没有处理器，没有兜底就是刷新即 404。
func TestStaticServesUnknownPath(t *testing.T) {
	ts := newTestServer(t, httpd.Options{Static: fakeUIHandler(t)})

	for _, path := range []string{"/", "/overview", "/servers/abc", "/assets/index-a1b2c3.js"} {
		resp, body := do(t, http.MethodGet, ts.URL+path, nil)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s: 状态码 = %d，想要 200", path, resp.StatusCode)
			continue
		}
		ct := resp.Header.Get("Content-Type")
		if strings.Contains(path, "/assets/") {
			if strings.Contains(ct, "html") {
				t.Errorf("GET %s: 静态资源被回退成 HTML 了（Content-Type=%q）", path, ct)
			}
			continue
		}
		if !strings.Contains(string(body), "app shell") {
			t.Errorf("GET %s: 未回退到入口页: %q", path, body)
		}
	}
}

// TestStaticReservedNamespaceStays404 保留命名空间下的未命中不回退。
//
// /api/does-not-exist 得到 200 + HTML 是这类架构里最常见也最难查的
// 故障之一：客户端会报"JSON 解析失败"，而真正的事实是"接口不存在"。
// 200 还让路径拼错在监控里彻底隐形。这条用例把这个边界钉住，
// 同时确认它没有扩得太宽——/apifoo 不是 /api 的子路径，照常回退。
func TestStaticReservedNamespaceStays404(t *testing.T) {
	ts := newTestServer(t, httpd.Options{Static: fakeUIHandler(t)})

	for _, path := range []string{
		"/api", "/api/", "/api/nope", "/api/does-not-exist",
		// /healthz、/readyz 是精确路由，其子树没有节点，会落到 NoRoute。
		"/healthz/nope", "/readyz/nope",
		// 注意这里没有 /mcp/... 与 /sse/...：它们由 `Any("/mcp/*path")`
		// 这类通配路由接住，根本不会走到 NoRoute，测不到这条边界。
		// 那是有意的（部分客户端会带子路径），不是遗漏。
	} {
		resp, body := do(t, http.MethodGet, ts.URL+path, nil)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s: 状态码 = %d，想要 404（body=%q）", path, resp.StatusCode, body)
		}
		if strings.Contains(string(body), "app shell") {
			t.Errorf("GET %s: 保留命名空间不该回退到界面", path)
		}
	}

	// 边界不能扩得过宽：这是个正常的界面路由。
	resp, body := do(t, http.MethodGet, ts.URL+"/apifoo", nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "app shell") {
		t.Errorf("GET /apifoo: 状态码=%d，想要 200 + 界面（它不属于 /api 命名空间）", resp.StatusCode)
	}
}

// TestNoStaticKeeps404 不挂界面时行为不变：未命中就是干净的 404。
//
// 这条守的是"没跑 make ui 的开发者与纯 API 部署"：
// 界面内嵌与否，都不该改变 /api 的契约。
func TestNoStaticKeeps404(t *testing.T) {
	ts := newTestServer(t, httpd.Options{})

	resp, body := do(t, http.MethodGet, ts.URL+"/overview", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("未挂界面时 GET /overview: 状态码 = %d，想要 404", resp.StatusCode)
	}
	if strings.Contains(string(body), "app shell") {
		t.Error("未挂界面却返回了界面内容")
	}

	// 具体路由不受影响。
	resp, _ = do(t, http.MethodGet, ts.URL+"/api/overview", nil)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /api/overview: 状态码 = %d，想要 200", resp.StatusCode)
	}
}
