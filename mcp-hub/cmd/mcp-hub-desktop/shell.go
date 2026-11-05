package main

import (
	"context"

	"go.uber.org/zap"

	"github.com/hydra/mcp-hub/internal/engine"
)

// Shell 是"把跑起来的引擎交给用户"的那一层。
//
// 它是引擎与窗口之间唯一需要定型的接缝，也是"桌面形态"这个词
// 唯一有分歧的地方。当前有两个实现，由构建标签二选一：
//
//	shell_browser.go  (默认)      起本地服务 + 打开系统默认浏览器
//	shell_wails.go    (-tags wails) Wails v3 原生窗口
//
// 两者的差别只有"界面画在哪"：引擎、数据库、缓存、迁移、探活、
// 优雅退出全部走 internal/engine，是同一份实现。这正是把装配根
// 抽出去时要换来的东西——再加一个 Tauri 壳也不需要动业务代码。
//
// 注意实现拿到的不是"一个地址"而是整个 *engine.Engine：
// 浏览器壳只需要 info.BaseURL，而原生窗口要的是 eng.Handler()——
// 把后端直接挂进 webview 的资源服务器，界面与 /api、/mcp 同源，
// 连端口都不必经过。
type Shell interface {
	// Run 阻塞直到用户结束应用（关窗 / Ctrl-C / 引擎异常退出）。
	// 返回 nil 表示正常结束，返回 context.Canceled 表示被外部取消。
	Run(ctx context.Context, eng *engine.Engine, info engine.Info) error
}

// shellReady 是两种壳共用的启动播报。
//
// 放在这里而不是各自的实现里：无论是窗口还是浏览器，用户接下来要
// 做的事是同一件——把 MCP 地址配进客户端。这段话必须一致。
func shellReady(log *zap.Logger, info engine.Info, shellName string) {
	log.Info("桌面端已就绪",
		zap.String("shell", shellName),
		zap.String("ui", info.BaseURL),
		zap.String("mcp", info.MCPURL),
		zap.String("db", info.DBDialect),
		zap.String("dbLocator", info.DBLocator),
		zap.String("cache", info.CacheDialect),
	)
}
