package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hydra/mcp-hub/internal/cache"
	"github.com/hydra/mcp-hub/internal/httpd"
	"github.com/hydra/mcp-hub/internal/hub"
	"github.com/hydra/mcp-hub/internal/hub/retrieval"
	"github.com/hydra/mcp-hub/internal/store"
	"github.com/spf13/cobra"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

const defaultProbeInterval = 45 * time.Second

// serve 的配置全在 .env，不收任何参数。
func newServeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "启动网关：聚合下游工具并对外提供 MCP 端点",
		Long: `启动网关：聚合下游工具并对外提供 MCP 端点。

serve 的配置全部来自 .env 与进程环境变量，不接受任何命令行参数。
可配置项见 mcp-hub env。`,
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
	cfg, err := LoadSettings()
	if err != nil {
		fmt.Fprintf(os.Stderr, "配置错误: %v\n", err)
		return 2
	}

	logger := newLogger(cfg.LogLevel)

	for _, w := range cfg.Warnings {
		logger.Warn("配置可疑", zap.String("detail", w))
	}

	st, err := store.Open(cfg.DB)
	if err != nil {
		fmt.Fprintf(os.Stderr, "打开数据库失败: %v\n", err)
		return 3
	}
	defer st.Close()

	c, err := openCache(cfg.Cache)
	if err != nil {
		fmt.Fprintf(os.Stderr, "打开缓存失败: %v\n", err)
		return 3
	}
	logger.Info("缓存后端", zap.String("backend", "redis"))

	svc := hub.NewServiceWithOptions(st, logger, hub.Options{
		Cache:           c,
		ListMode:        cfg.ListMode,
		BlockAfterFails: cfg.ProbeBlockAfter,
	})
	defer svc.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// 没配 EMBED_API_KEY 就不走向量检索，自动降级 bm25。
	em := retrieval.NewEmbedManager(st, logger, retrieval.EmbedOptions{
		BaseURL:       cfg.EmbedBaseURL,
		APIKey:        cfg.EmbedAPIKey,
		Model:         cfg.EmbedModel,
		Dimensions:    cfg.EmbedDims,
		Batch:         cfg.EmbedBatch,
		CorpusFields:  cfg.CorpusFields,
		RerankModel:   cfg.RerankModel,
		RerankBaseURL: cfg.RerankBaseURL,
		RerankAPIKey:  cfg.RerankAPIKey,
	})
	svc.AttachEmbedding(em)
	if em.Embedder() != nil {
		// 后台补齐向量，不阻塞启动。
		go func() {
			st := em.Reconcile(ctx)
			fields := []zap.Field{
				zap.String("state", string(st.State)),
				zap.Int("total", st.Total),
				zap.Int("done", st.Done),
				zap.Int("failed", st.Failed),
				zap.Int("covered", st.Covered),
				zap.String("message", st.Message),
			}
			if st.Ready {
				logger.Info("向量对账完成", fields...)
				return
			}
			logger.Warn("向量对账未完全就绪，检索暂时降级 keyword/bm25", fields...)
		}()
	}

	if servers, err := svc.Servers(ctx); err == nil {
		enabled := 0
		for _, sv := range servers {
			if sv.Enabled {
				enabled++
			}
		}
		logger.Info("已从数据库加载",
			zap.Int("servers", len(servers)), zap.Int("enabled", enabled))
	}

	if cfg.ProbeOnStart {
		report := svc.RefreshAll(ctx)
		logger.Info("启动探活完成",
			zap.Int("servers", report.Total),
			zap.Int("healthy", report.Healthy),
			zap.Int("failed", report.Failed),
			zap.Int("tools", report.ToolNum),
			zap.Int64("elapsedMs", report.Elapsed.Milliseconds()),
		)
	}

	srv := httpd.New(svc, httpd.Options{
		Addr:      cfg.Addr,
		Logger:    logger,
		EnableSSE: cfg.EnableSSE,
		APIKey:    cfg.APIKey,
		GinMode:   cfg.GinMode,
	})

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Start() }()

	base := waitForAddr(srv, errCh)
	if base == "" {
		return 3
	}

	if cfg.APIKey == "" && !isLoopbackAddr(cfg.Addr) {
		logger.Warn("管理接口已监听在非本机地址，但未设置 MCPHUB_API_KEY",
			zap.String("addr", cfg.Addr), zap.String("risk", "任何能访问该地址的人都可以增删下游 server"))
	}

	printBanner(base, svc, bannerOptions{sse: cfg.EnableSSE, apiKey: cfg.APIKey != ""})

	if cfg.ProbeInterval > 0 {
		svc.StartProbeScheduler(ctx, cfg.ProbeInterval)
	}

	select {
	case err := <-errCh:
		if err != nil {
			logger.Error("HTTP 服务异常退出", zap.Error(err))
			return 3
		}
	case <-ctx.Done():
		logger.Info("收到中断信号，开始优雅关闭")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("优雅关闭失败", zap.Error(err))
		return 3
	}
	return 0
}

// openCache 只负责连接：连接串形态已由配置层（kindRedisURL）判过。
func openCache(spec string) (cache.Store, error) {
	return cache.NewRedis(context.Background(), cache.RedisOptions{URL: spec})
}

// 等 HTTP 服务 bind 完成后，返回能直接访问的地址。
func waitForAddr(srv *httpd.Server, errCh <-chan error) string {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-errCh:
			if err != nil {
				fmt.Fprintf(os.Stderr, "启动失败: %v\n", err)
			}
			return ""
		default:
		}

		addr := srv.Addr()
		if addr != "" && !httpd.IsUnbound(addr) {
			return httpd.NormalizeBase(addr)
		}
		time.Sleep(10 * time.Millisecond)
	}
	return ""
}

func printBanner(base string, svc *hub.Service, opts bannerOptions) {
	snap := svc.Registry().Current()

	fmt.Printf("\n  MCP Hub 已就绪\n\n")
	fmt.Printf("  MCP 端点   %s/mcp\n", base)
	fmt.Printf("  健康检查   %s/healthz\n", base)
	fmt.Printf("  就绪检查   %s/readyz\n", base)
	fmt.Printf("  管理接口   %s/api/overview\n", base)
	if opts.sse {
		fmt.Printf("  旧版 SSE   %s/sse\n", base)
	}
	fmt.Printf("\n  已聚合 %d 个工具，来自 %d 个下游 server\n", snap.ToolCount(), len(snap.Servers()))

	fmt.Printf("\n  客户端接入配置:\n")
	fmt.Printf("  { \"mcpServers\": { \"mcp-hub\": { \"url\": \"%s/mcp\" } } }\n\n", base)

	if opts.apiKey {
		fmt.Printf("  管理接口已开启鉴权，请求需带 X-API-Key 头。\n\n")
	}
}

type bannerOptions struct {
	sse    bool
	apiKey bool
}

// isLoopbackAddr 拿不准就按"对外暴露"算，宁可多警告一次。
func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil || host == "" {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
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
