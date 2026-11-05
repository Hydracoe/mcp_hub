// Package webassets 提供"把管理界面内嵌进二进制"这件事。
//
// 它是部署形态的一部分，不是业务的一部分：谁来决定"界面从哪来"
// 是入口（cmd/*），本包只负责"给定一份构建产物，把它正确地服务出去"。
//
// # 产物从哪来
//
// dist/ 是 mcp-hub-admin 的构建产物拷贝。Go 的 go:embed 不能跨 `..`，
// 所以不能直接嵌 ../mcp-hub-admin/dist——由构建流程拷进来：
//
//	make ui        构建前端并拷贝到 internal/webassets/dist
//	make ui-clean  删掉产物，还原成"没有界面"的状态
//
// 仓库里 dist/ 下只有一个 .placeholder 空标记，它的唯一作用是让
// go:embed 在"还没跑过 make ui"时有东西可嵌（embed 不接受空目录）。
// 这不是"fake it"：Available() 会如实报告有没有真实产物，入口据此
// 决定要不要把界面挂出去，并且会在横幅里说清楚。
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
// 判据就是"入口文件在不在"：dist/index.html 是前端构建的产物清单，
// 它不存在就等于整包没构建过。用一个标记字符串去认占位页曾经可以，
// 但那要求占位页本身必须是一份 index.html——于是"没有界面"这种状态
// 会被伪装成"有一个界面，只是内容很敷衍"。宁可让它缺席。
//
// 它的用途是让入口决定"要不要把界面挂出去"：没产物时挂上去只会得到
// 一张说明页，不如不挂，让 404 说清楚"这里没有界面"。
func Available() bool {
	_, err := fs.Stat(FS(), "index.html")
	return err == nil
}

// Handler 返回管理界面的 http.Handler。
//
// 行为对齐"静态站点 + SPA 回退"：
//   - 命中真实文件就返回它（带 http.FileServer 的缓存头与 If-Modified-Since 支持）；
//   - 其余一律回退到 index.html。
//
// 第二条是必须的：前端资源与 /api、/mcp 共用同一个端口，而这台服务器
// 在 /overview 这类路径上没有任何处理器。没有回退，刷新即 404。
//
// 注意本 handler 只该挂在路由表的**最后**（gin 的 NoRoute）。
// 挂在前面会把 /api 一起吞掉——它不认识路径语义，只认识文件。
func Handler() http.Handler { return handlerFor(FS()) }

// handlerFor 是 Handler 的可注入版本。
//
// 回退规则是这个包里唯一有分支的逻辑，而 go:embed 是编译期的——
// 不把它拆出来，就只能等到真的 `make ui` 之后才有东西可测。
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
// Vite 把带哈希的 chunk、css、字体都放在 assets/ 下，文件名里的哈希
// 由内容算出：内容一变文件名就变，所以同一个 URL 的内容永远不变，
// 可以放心 immutable。入口页反过来——它是资源清单，必须每次校验。
//
// 只认 assets/ 这一层是有意的：dist 根下还有从 public/ 原样拷过来的
// 文件（favicon.ico 之类），它们不带哈希，长缓存会让替换失效。
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
	// 不缓存入口页：它是资源清单，缓存住会让"部署了新版本却还加载旧 chunk"
	// 变成常态。带哈希的 assets/* 反过来可以长缓存（由 FileServer 处理）。
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(b)
}
