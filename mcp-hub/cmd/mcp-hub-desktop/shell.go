package main

import (
	"context"

	"go.uber.org/zap"

	"github.com/hydra/mcp-hub/internal/engine"
)

// Shell 把跑起来的引擎交给用户，是引擎与窗口之间唯一的接缝。
//
// 实现由构建标签二选一：shell_browser.go（默认）/ shell_wails.go（-tags wails）。
// 实现拿到整个 *engine.Engine：浏览器壳只用 info.BaseURL，原生窗口用
// eng.Handler() 把后端挂进 webview，界面与 /api、/mcp 同源，不经过端口。
type Shell interface {
	// Run 阻塞直到用户结束应用（关窗 / Ctrl-C / 引擎异常退出）。
	// 返回 nil 表示正常结束，返回 context.Canceled 表示被外部取消。
	Run(ctx context.Context, eng *engine.Engine, info engine.Info) error
}

// shellReady 是两种壳共用的启动播报：用户接下来要做的事相同——把 MCP 地址
// 配进客户端，所以这段输出必须一致。
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
