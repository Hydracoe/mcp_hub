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
// 关键差别是界面不经过 TCP：eng.Handler() 直接交给 Wails 的资源服务器，
// 所有请求（HTML、chunk、/api、/mcp）在进程内打到同一台 gin，因此同源、
// 不受端口占用影响。Wails 自身的 /wails/* 端点由其中间件在 handler 之前处理。
type wailsShell struct {
	log *zap.Logger
}

func (s *wailsShell) Run(ctx context.Context, eng *engine.Engine, info engine.Info) error {
	handler := eng.Handler()
	if handler == nil {
		return errors.New("引擎尚未启动，没有可挂载的 HTTP 处理器")
	}
	// 进事件循环前先检查信号：此时 app.Quit() 还是空操作（平台实现未建立），
	// 否则 Run 会卡在永远退不出的窗口里。
	if err := ctx.Err(); err != nil {
		return err
	}

	app := application.New(application.Options{
		Name:        "MCP Hub",
		Description: "本机 MCP 网关：聚合下游 server 的工具，并对外提供 MCP 端点",
		// Wails 的系统日志接到同一份 zap：双击启动时 stderr 无人可见，
		// desktop.log 是唯一现场。
		Logger:   slog.New(newZapSlogHandler(s.log)),
		LogLevel: slog.LevelInfo,

		Assets: application.AssetOptions{
			Handler: handler,
		},

		Mac: application.MacOptions{
			// 关掉最后一个窗口就退出：桌面形态没有"后台常驻"的语义。
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},

		// 引擎必须在进程消失前停干净，否则 stdio 子进程会成孤儿。
		// macOS 的 [NSApp run] 不保证回到 Go，main 的 defer 可能不执行；
		// OnShutdown 是框架明确会阻塞等待的钩子。
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
		// URL 留空 = 资源服务器的根：文档源 macOS 是 wails://wails/、
		// Windows 是 http://wails.localhost/，写死任一平台都会对不上。
	})

	shellReady(s.log, info, "wails")

	// 外部原因要结束应用时，把窗口一起关掉。
	//
	// eng.Errors() 这一路尤其重要：引擎挂了而窗口还开着，界面点哪儿都没反应。
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
// MCP 地址是用户唯一要抄走的东西，且随端口变化，放在日志里不合适。
func windowTitle(info engine.Info) string {
	title := "MCP Hub"
	if info.MCPURL != "" {
		title += " — " + strings.TrimPrefix(info.MCPURL, "http://")
	}
	return title
}
