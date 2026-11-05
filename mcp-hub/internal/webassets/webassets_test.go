package webassets

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// fakeDist 模拟一份真实的前端产物：一个入口页 + 一个带哈希的资源。
func fakeDist() fstest.MapFS {
	return fstest.MapFS{
		"index.html":              &fstest.MapFile{Data: []byte("<html>app shell</html>")},
		"assets/index-a1b2c3.js":  &fstest.MapFile{Data: []byte("console.log(1)")},
		"assets/index-d4e5f6.css": &fstest.MapFile{Data: []byte("body{}")},
		"favicon.ico":             &fstest.MapFile{Data: []byte("ico")},
		"nested/deeply/asset.bin": &fstest.MapFile{Data: []byte("bin")},
		"assets/":                 &fstest.MapFile{Mode: fs.ModeDir},
	}
}

// TestHandlerServesRealFiles 真实文件必须原样返回，且不能被回退吞掉。
func TestHandlerServesRealFiles(t *testing.T) {
	h := handlerFor(fakeDist())

	for path, want := range map[string]string{
		"/assets/index-a1b2c3.js":  "console.log(1)",
		"/favicon.ico":             "ico",
		"/nested/deeply/asset.bin": "bin",
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("%s: 状态码 = %d，想要 200", path, rec.Code)
			continue
		}
		if got := rec.Body.String(); got != want {
			t.Errorf("%s: body = %q，想要 %q", path, got, want)
		}
	}
}

// TestHandlerSPAFallback 是这个包存在的理由：前端路由要能刷新。
//
// /overview 这类路径后端没有任何处理器，必须回退到入口页。
func TestHandlerSPAFallback(t *testing.T) {
	h := handlerFor(fakeDist())

	for _, path := range []string{"/overview", "/servers/abc", "/deep/unknown/path"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("%s: 状态码 = %d，想要 200（回退到入口页）", path, rec.Code)
			continue
		}
		if body := rec.Body.String(); !strings.Contains(body, "app shell") {
			t.Errorf("%s: 回退了但内容不是入口页: %q", path, body)
		}
	}
}

// TestHandlerDirectoryFallsBackToIndex 目录不能泄漏成列表页。
//
// fs.Stat 命中了 assets/ 但它是目录，交给 FileServer 会得到一份文件清单。
func TestHandlerDirectoryFallsBackToIndex(t *testing.T) {
	h := handlerFor(fakeDist())

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/assets/", nil))
	if body := rec.Body.String(); !strings.Contains(body, "app shell") {
		t.Errorf("/assets/ 应回退到入口页，实际: %q", body)
	}
	if strings.Contains(rec.Body.String(), "index-a1b2c3.js") {
		t.Error("/assets/ 泄漏了目录列表")
	}
}

// TestHandlerIndexNotCached 入口页是资源清单，不能被缓存。
//
// 否则部署新版本后浏览器仍加载旧 chunk；带哈希资源反过来应长缓存。
func TestHandlerIndexNotCached(t *testing.T) {
	h := handlerFor(fakeDist())

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("入口页 Cache-Control = %q，想要 no-cache", got)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Errorf("入口页 Content-Type = %q，想要 text/html", got)
	}
}

// TestHandlerHashedAssetsImmutable 带哈希的资源必须长缓存。
//
// http.FileServer 自己不设 Cache-Control；少了这一条每次刷新都要重新验证。
// dist 根下的 favicon.ico 之类不带哈希，绝不能长缓存。
func TestHandlerHashedAssetsImmutable(t *testing.T) {
	h := handlerFor(fakeDist())

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/assets/index-a1b2c3.js", nil))
	if got := rec.Header().Get("Cache-Control"); got != immutableCacheControl {
		t.Errorf("assets/ 下资源 Cache-Control = %q，想要 %q", got, immutableCacheControl)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/favicon.ico", nil))
	if got := rec.Header().Get("Cache-Control"); strings.Contains(got, "immutable") {
		t.Errorf("不带哈希的 favicon.ico 不该被标记 immutable（Cache-Control=%q）", got)
	}
}

// TestHandlerRejectsNonReadMethods 只服务读请求。
//
// 挂到 NoRoute 后任何未匹配的方法都会走到这里；对 POST 回 200 + HTML
// 会让客户端误以为写成功。
func TestHandlerRejectsNonReadMethods(t *testing.T) {
	h := handlerFor(fakeDist())

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, "/overview", nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s /overview: 状态码 = %d，想要 404", method, rec.Code)
		}
	}

	// HEAD 是读请求，应放行。
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodHead, "/", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("HEAD /: 状态码 = %d，想要 200", rec.Code)
	}
}

// TestHandlerWithoutIndex 没有入口页时全部 404，而不是空 200。
//
// 静默回空页面会让排查跑偏；handler 不该依赖调用方替它把关。
func TestHandlerWithoutIndex(t *testing.T) {
	h := handlerFor(fstest.MapFS{
		"assets/index-a1b2c3.js": &fstest.MapFile{Data: []byte("x")},
	})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("/: 状态码 = %d，想要 404", rec.Code)
	}
}

// TestEmbeddedDistShape 检查内嵌的 dist/ "要么空，要么是一份完整构建"。
//
// 不去断言"仓库里没有产物"——那是会变的工作树状态。守两条不变量：
// Available() 与 index.html 是否存在一致；有 index.html 就必须有 assets/ 内容。
//
// 另守住前端 base: './'：入口页必须用相对路径引用资源，否则子路径反代与
// wails:// 资源服务器都会白屏。
func TestEmbeddedDistShape(t *testing.T) {
	_, err := fs.Stat(FS(), "index.html")
	hasIndex := err == nil

	if Available() != hasIndex {
		t.Fatalf("Available()=%v 与 index.html 是否存在(%v)不一致", Available(), hasIndex)
	}

	if !hasIndex {
		t.Log("dist/ 里没有前端产物（还没跑过 make ui）—— 引擎不会挂载界面，这是合法形态")
		return
	}

	entries, err := fs.ReadDir(FS(), "assets")
	if err != nil {
		t.Fatalf("有 index.html 但读不到 assets/：%v —— 这不是一份真实构建产物", err)
	}
	if len(entries) == 0 {
		t.Fatal("有 index.html 但 assets/ 是空的 —— 这不是一份真实构建产物")
	}

	b, err := fs.ReadFile(FS(), "index.html")
	if err != nil {
		t.Fatalf("读 index.html 失败: %v", err)
	}
	for _, abs := range []string{`"/assets/`, `'/assets/`} {
		if strings.Contains(string(b), abs) {
			t.Error("index.html 用了绝对资源路径 —— 检查 mcp-hub-admin/vite.config.ts 的 base 是否为 './'")
			break
		}
	}
}
