package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/hydra/mcp-hub/internal/config"
	"github.com/hydra/mcp-hub/internal/engine"
	"github.com/hydra/mcp-hub/internal/hub"
)

// serve 的配置全在 .env / 进程环境变量，不收任何参数。
//
// 本命令现在只剩三件事：读配置、起引擎、等信号。装配的全部细节在
// internal/engine —— 桌面入口（cmd/mcp-hub-desktop）用的是同一个引擎，
// 两条路线的业务行为因此不会有第二份实现。
func newServeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "启动网关：聚合下游工具并对外提供 MCP 端点",
		Long: `启动网关：聚合下游工具并对外提供 MCP 端点。

serve 的配置全部来自 .env 与进程环境变量，不接受任何命令行参数。
可配置项见 mcp-hub env。

数据库与缓存后端都可在配置里显式指定（MCPHUB_DB_DRIVER / MCPHUB_CACHE_DRIVER），
不指定时按连接串形态推断：postgres:// 走 PostgreSQL，文件路径走 SQLite；
redis:// 走 Redis，留空走进程内缓存。`,
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				fmt.Fprintf(cmd.ErrOrStderr(), "serve 不接受参数 %q —— 配置请写进 .env（mcp-hub env 可列出全部配置项）\n\n", args[0])
				fmt.Fprint(cmd.ErrOrStderr(), cmd.UsageString())
				return &exitError{code: 2}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return codeToErr(runServe())
		},
	}
	cmd.SetFlagErrorFunc(func(c *cobra.Command, err error) error {
		fmt.Fprintf(c.ErrOrStderr(), "%v\n\nserve 的配置全部来自环境变量与 .env，没有命令行参数（mcp-hub env 可列出全部配置项）\n\n", err)
		fmt.Fprint(c.ErrOrStderr(), c.UsageString())
		return &exitError{code: 2}
	})
	return cmd
}

func runServe() int {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "配置错误: %v\n", err)
		return 2
	}

	logger := newLogger(cfg.LogLevel)
	for _, w := range cfg.Warnings {
		logger.Warn("配置可疑", zap.String("detail", w))
	}

	eng, err := engine.New(engine.Options{
		Settings: cfg,
		Logger:   logger,
		DataDir:  dataDir(),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "启动失败: %v\n", err)
		return 3
	}
	// 关闭必须走引擎的优雅路径：stdio 子进程挂在这一层，
	// 直接 os.Exit 会留下孤儿进程。
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), engine.ShutdownTimeout)
		defer cancel()
		if err := eng.Stop(ctx); err != nil {
			logger.Error("关闭失败", zap.Error(err))
		}
	}()

	info, err := eng.Start()
	if err != nil {
		fmt.Fprintf(os.Stderr, "启动失败: %v\n", err)
		return 3
	}
	printBanner(info, eng.Service())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	select {
	case err := <-eng.Errors():
		if err != nil {
			logger.Error("HTTP 服务异常退出", zap.Error(err))
			return 3
		}
	case <-ctx.Done():
		logger.Info("收到中断信号，开始优雅关闭")
	}
	return 0
}

// dataDir 是命令行形态下的数据目录：进程的当前工作目录。
//
// 桌面形态用的是系统的应用数据目录（见 cmd/mcp-hub-desktop），
// 这个差别是两种形态唯一"数据放哪"的分歧点，所以由入口回答，
// 不写死在 store 里。
func dataDir() string {
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}
	return dir
}

func printBanner(info engine.Info, svc *hub.Service) {
	snap := svc.Registry().Current()

	fmt.Printf("\n  MCP Hub 已就绪\n\n")
	// 界面放在第一行：这是绝大多数人真正要点的东西，其余是给客户端和
	// 运维用的。没内嵌界面时这一行整行不出现——打一个点开就是 404 的
	// 地址比不打更糟，它会把"没构建前端"误报成"服务坏了"。
	if info.UIEmbedded {
		fmt.Printf("  管理界面   %s\n", info.UIURL)
	}
	fmt.Printf("  MCP 端点   %s\n", info.MCPURL)
	fmt.Printf("  健康检查   %s\n", info.HealthzURL)
	fmt.Printf("  就绪检查   %s\n", info.ReadyzURL)
	fmt.Printf("  管理接口   %s\n", info.OverviewURL)
	if info.EnableSSE {
		fmt.Printf("  旧版 SSE   %s/sse\n", info.BaseURL)
	}
	fmt.Printf("\n  运行形态   数据库 %s · 缓存 %s\n", info.DBDialect, cacheLabel(info))
	fmt.Printf("  已聚合 %d 个工具，来自 %d 个下游 server\n", snap.ToolCount(), len(snap.Servers()))

	fmt.Printf("\n  客户端接入配置:\n")
	fmt.Printf("  { \"mcpServers\": { \"mcp-hub\": { \"url\": \"%s\" } } }\n\n", info.MCPURL)

	if info.APIKeySet {
		fmt.Printf("  管理接口已开启鉴权，请求需带 X-API-Key 头。\n\n")
	}

	if !info.UIEmbedded {
		// 说清楚"这不是故障，是构建方式"：纯 API 部署本来就该是这样，
		// 而想用界面的人需要一个能照抄的命令。
		fmt.Printf("  这个二进制没有内嵌管理界面（构建时未执行 make ui）。\n")
		fmt.Printf("  接口不受影响：/api 与 /mcp 均正常工作。带上界面请执行:\n")
		fmt.Printf("      make ui && make build\n\n")
	}
}

func cacheLabel(info engine.Info) string {
	if info.CacheShared {
		return info.CacheDialect
	}
	return info.CacheDialect + "（不跨进程共享）"
}

func newLogger(level string) *zap.Logger {
	var lv zapcore.Level
	switch level {
	case "debug":
		lv = zapcore.DebugLevel
	case "warn":
		lv = zapcore.WarnLevel
	case "error":
		lv = zapcore.ErrorLevel
	default:
		lv = zapcore.InfoLevel
	}
	cfg := zap.NewProductionConfig()
	cfg.Level = zap.NewAtomicLevelAt(lv)
	logger, err := cfg.Build()
	if err != nil {
		panic(fmt.Sprintf("构造日志器失败: %v", err))
	}
	return logger
}
