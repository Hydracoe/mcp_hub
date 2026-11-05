//go:build wails

package main

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/wailsapp/wails/v3/pkg/application"
	"go.uber.org/zap"

	"github.com/hydra/mcp-hub/internal/engine"
)

func newShell(log *zap.Logger) Shell { return &wailsShell{log: log} }

// wailsShell 用 Wails v3 开一个原生窗口，把管理界面画在 webview 里。
//
// 与 browserShell 的关键差别不只是"窗口长得像原生应用"，而是
// **界面不再经过 TCP 端口**：eng.Handler() 直接交给 Wails 的资源
// 服务器，webview 的每个请求（HTML、chunk、/api、/mcp）都在进程内
// 打到同一台 gin 上。由此带来三件事：
//
//	同源         前端 fetch('/api/...') 一行不用改，CORS 也不必开
//	端口无关     MCP 端口被占用也照样能打开界面
//	角色变单一   TCP 端口只服务于外部 AI 客户端，不再兼职"给界面用"
//
// Wails 自己的运行时端点（/wails/runtime.js、/wails/transport.js、
// 事件与 GoStream 的轮询路径）由它自己的中间件挡在 handler 之前，
// 不会落到 gin 上——本项目的 gin 引擎没有 /wails 路由，也不需要新增。
type wailsShell struct {
	log *zap.Logger
}

func (s *wailsShell) Run(ctx context.Context, eng *engine.Engine, info engine.Info) error {
	handler := eng.Handler()
	if handler == nil {
		return errors.New("引擎尚未启动，没有可挂载的 HTTP 处理器")
	}
	// 进入事件循环之前先看一眼：信号如果在启动阶段就来了，
	// 此时 app.Quit() 还是空操作（平台实现尚未建立，Quit 会因
	// impl == nil 直接返回），Run 会卡在一个永远退不出的窗口里。
	if err := ctx.Err(); err != nil {
		return err
	}

	app := application.New(application.Options{
		Name:        "MCP Hub",
		Description: "本机 MCP 网关：聚合下游 server 的工具，并对外提供 MCP 端点",
		// Wails 的系统日志（资源加载失败、运行时错误）接到同一份
		// zap 上：双击启动时 stderr 没人看，desktop.log 是唯一的现场。
		Logger:   slog.New(newZapSlogHandler(s.log)),
		LogLevel: slog.LevelInfo,

		Assets: application.AssetOptions{
			Handler: handler,
		},

		Mac: application.MacOptions{
			// 关掉最后一个窗口就退出：桌面形态没有"后台常驻"的语义，
			// 留一个没有窗口的进程在活动监视器里只会让人困惑。
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},

		// 引擎必须在进程真的消失之前停干净：stdio 子进程挂在它上面，
		// 让进程直接退会留下一堆孤儿。
		//
		// 放在 OnShutdown 而不是"等 Run() 返回后走 main 的 defer"：
		// macOS 上 Run() 走的是 [NSApp run]，应用终止不保证回到 Go，
		// 那条 defer 有可能永远执行不到。OnShutdown 是框架明确承诺
		// 会阻塞等待的钩子，位置才对。
		OnShutdown: func() { s.stopEngine(eng) },

		ErrorHandler: func(err error) {
			s.log.Error("Wails 运行时错误", zap.Error(err))
		},
	})

	app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:      "main",
		Title:     windowTitle(info),
		Width:     1280,
		Height:    860,
		MinWidth:  960,
		MinHeight: 640,
		// URL 留空 = 资源服务器的根。不写 "/" 也不写完整地址：
		// 文档源在 macOS 上是 wails://wails/、在 Windows 上是
		// http://wails.localhost/，写死任何一个都会在另一个平台上对不上。
	})

	shellReady(s.log, info, "wails")

	// 外部原因要结束应用时，把窗口一起关掉。
	//
	// eng.Errors() 这一路尤其重要：引擎自己挂了而窗口还开着，
	// 用户面对的是一个点哪儿都没反应的界面，比直接消失更难排查。
	go func() {
		var reason error
		select {
		case <-ctx.Done():
			reason = ctx.Err()
		case err := <-eng.Errors():
			reason = err
		}
		if reason != nil {
			s.log.Error("关闭窗口", zap.Error(reason))
		}
		app.Quit()
	}()

	return app.Run()
}

func (s *wailsShell) stopEngine(eng *engine.Engine) {
	ctx, cancel := context.WithTimeout(context.Background(), engine.ShutdownTimeout)
	defer cancel()
	if err := eng.Stop(ctx); err != nil {
		s.log.Error("关闭引擎失败", zap.Error(err))
		return
	}
	s.log.Info("引擎已停止")
}

// windowTitle 把最有用的那条运行事实放进标题栏。
//
// MCP 地址是用户接下来唯一要抄走的东西（配进 AI 客户端），而它随端口
// 变化、藏在日志里不合适。顺带带上后端，省掉"这库到底存哪了"的疑问。
func windowTitle(info engine.Info) string {
	title := "MCP Hub"
	if info.MCPURL != "" {
		title += " — " + strings.TrimPrefix(info.MCPURL, "http://")
	}
	return title
}
