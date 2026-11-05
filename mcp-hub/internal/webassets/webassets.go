// Package webassets 把管理界面的构建产物内嵌进二进制。
//
// dist/ 是 mcp-hub-admin 构建产物的拷贝（embed 不能跨 ..，由 make ui 拷入，
// make ui-clean 还原）。仓库里只有 .placeholder 让 embed 有东西可嵌；
// Available() 如实报告有无真实产物，入口据此决定是否挂载界面。
package webassets

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:dist
var distFS embed.FS

// FS 返回构建产物的根目录。
//
// 没有产物时它只含一个 .placeholder，任何路径都读不到——调用方
// 必须先问 Available()，而不是假定里面有什么。
func FS() fs.FS {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		// go:embed 保证了 dist 存在，走不到这里。
		return distFS
	}
	return sub
}

// Available 报告当前嵌入的是否为真实前端产物。
//
// 判据是 dist/index.html 是否存在。入口据此决定要不要挂载界面：
// 没产物时挂上去只会得到一张说明页，不如让 404 说清楚。
func Available() bool {
	_, err := fs.Stat(FS(), "index.html")
	return err == nil
}

// Handler 返回管理界面的 http.Handler。
//
// 命中真实文件就返回它（带 FileServer 的缓存头与 If-Modified-Since），
// 其余一律回退到 index.html：前端资源与 /api、/mcp 共用端口，刷新才不会 404。
//
// 只该挂在路由表最后（gin 的 NoRoute），挂在前面会把 /api 一起吞掉——
// 它不认识路径语义，只认识文件。
func Handler() http.Handler { return handlerFor(FS()) }

// handlerFor 是 Handler 的可注入版本。
//
// 回退规则是唯一有分支的逻辑；不拆出来就只能等 make ui 之后才有东西可测。
func handlerFor(fsys fs.FS) http.Handler {
	fileServer := http.FileServer(http.FS(fsys))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.NotFound(w, r)
			return
		}

		path := strings.TrimPrefix(r.URL.Path, "/")
		switch {
		case path == "" || path == "index.html":
			serveIndex(fsys, w, r)
		default:
			// fs.Stat 而不是 Open：目录（assets/）也要回退到入口页，
			// 否则 /assets/ 会得到一份目录列表。
			if info, err := fs.Stat(fsys, path); err == nil && !info.IsDir() {
				if isImmutableAsset(path) {
					w.Header().Set("Cache-Control", immutableCacheControl)
				}
				fileServer.ServeHTTP(w, r)
				return
			}
			serveIndex(fsys, w, r)
		}
	})
}

const immutableCacheControl = "public, max-age=31536000, immutable"

// isImmutableAsset 判断某个路径是不是"内容寻址"的资源。
//
// Vite 把带哈希的 chunk、css、字体放在 assets/ 下，内容变则文件名变，
// 同一 URL 的内容永远不变，可放心 immutable。入口页是资源清单，必须每次校验。
//
// 只认 assets/ 这一层：dist 根下是从 public/ 原样拷来的文件（favicon.ico 等），
// 不带哈希，长缓存会让替换失效。
func isImmutableAsset(path string) bool {
	return strings.HasPrefix(path, "assets/")
}

// serveIndex 直接返回 index.html（用于 `/` 与所有未命中路径的回退）。
func serveIndex(fsys fs.FS, w http.ResponseWriter, r *http.Request) {
	b, err := fs.ReadFile(fsys, "index.html")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// 不缓存入口页（资源清单），否则部署新版本后仍会加载旧 chunk。
	// 带哈希的 assets/* 反过来可以长缓存（由 FileServer 处理）。
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(b)
}
