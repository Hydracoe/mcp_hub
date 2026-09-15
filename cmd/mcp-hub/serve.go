package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/hydra/mcp-hub/internal/cache"
	"github.com/hydra/mcp-hub/internal/httpd"
	"github.com/hydra/mcp-hub/internal/hub"
	"github.com/hydra/mcp-hub/internal/store"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// defaultProbeInterval 是周期探活的间隔。
//
// 45s 是个折中：更短能让下游故障更快被发现，但每次探活都是一次
// 完整的 tools/list 往返；更长则会让"下游已经恢复"这件事迟迟不生效。
const defaultProbeInterval = 45 * time.Second

// runServe 启动网关：加载配置 → 建聚合 → 对外服务 → 周期探活。
func runServe(args []string) int {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)

	var (
		dbPath        = fs.String("db", "mcp-hub.db", "数据库：SQLite 文件路径，或 postgres:// 连接串")
		cacheSpec     = fs.String("cache", "memory", "缓存后端：memory（进程内），或 redis:// 连接串（如 redis://127.0.0.1:6379/0）")
		addr          = fs.String("addr", ":9090", "监听地址，端口写 0 由系统分配")
		probeInterval = fs.Duration("probe-interval", defaultProbeInterval, "周期探活间隔，0 关闭")
		probeOnStart  = fs.Bool("probe-on-start", true, "启动时先探活一轮，再对外服务")
		noSSE         = fs.Bool("no-sse", false, "不暴露旧版 SSE 端点")
		apiKey        = fs.String("api-key", "", "管理接口的访问密钥；留空则不校验（仅建议监听 127.0.0.1 时留空）")
		logLevel      = fs.String("log-level", "info", "debug | info | warn | error")
		listMode      = fs.String("list-mode", "meta", "tools/list 返回什么：meta（只列 5 个检索工具，PRD §4.7.6）或 all（全量工具）")
		embedBaseURL  = fs.String("embed-base-url", "https://api.siliconflow.cn/v1", "embed 服务地址（OpenAI 兼容）")
		embedAPIKey   = fs.String("embed-api-key", "", "embed 服务密钥；缺省读环境变量 MCPHUB_EMBED_API_KEY")
		embedModel    = fs.String("embed-model", "BAAI/bge-m3", "embed 模型名")
		embedDims     = fs.Int("embed-dims", 1024, "embed 向量维度（需与模型一致）")
		rerankModel   = fs.String("rerank-model", "BAAI/bge-reranker-v2-m3", "rerank 模型名；留空关闭 rerank")
	)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *listMode != hub.ListModeMeta && *listMode != hub.ListModeAll {
		fmt.Fprintf(os.Stderr, "--list-mode 只能是 meta 或 all，收到 %q\n", *listMode)
		return 2
	}
	if key := *embedAPIKey; key == "" {
		*embedAPIKey = os.Getenv("MCPHUB_EMBED_API_KEY")
	}

	logger := newLogger(*logLevel)

	st, err := store.Open(*dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "打开数据库失败: %v\n", err)
		return 3
	}
	defer st.Close()

	backend, c, err := openCache(*cacheSpec)
	if err != nil {
		fmt.Fprintf(os.Stderr, "打开缓存失败: %v\n", err)
		return 3
	}
	logger.Info("缓存后端", zap.String("backend", backend))

	svc := hub.NewServiceWithOptions(st, logger, hub.Options{
		Cache:    c,
		ListMode: *listMode,
	})
	defer svc.Close()

	// 根上下文绑定中断信号，Ctrl-C 与 SIGTERM 走同一条关闭路径。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// 嵌入检索（PRD §4.7.5）：未配置密钥时 em 内部无客户端，
	// 检索自动降级 keyword/bm25，网关照常运行。
	em := hub.NewEmbedManager(st, logger, hub.EmbedOptions{
		BaseURL:     *embedBaseURL,
		APIKey:      *embedAPIKey,
		Model:       *embedModel,
		Dimensions:  *embedDims,
		RerankModel: *rerankModel,
	})
	svc.AttachEmbedding(em)
	if em.Embedder() != nil {
		// 初始构建 / 增量补齐在后台跑，不阻塞启动（PRD §4.7.5）。
		go em.Rebuild(ctx)
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

	if *probeOnStart {
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
		Addr:      *addr,
		Logger:    logger,
		EnableSSE: !*noSSE,
		APIKey:    *apiKey,
	})

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Start() }()

	// Start 是阻塞的，监听地址要等它真正 bind 之后才拿得到。
	base := waitForAddr(srv, errCh)
	if base == "" {
		return 3
	}

	// 管理接口能改配置，等于半个 shell。监听在非本机地址上却又不
	// 校验凭据，是一个必须当场说出来的配置问题——它太容易被忽略，
	// 而后果是任何人都能往这台机器上挂一个下游 server。
	if *apiKey == "" && !isLoopbackAddr(*addr) {
		logger.Warn("管理接口已监听在非本机地址，但未设置 --api-key",
			zap.String("addr", *addr), zap.String("risk", "任何能访问该地址的人都可以增删下游 server"))
	}

	printBanner(base, svc, bannerOptions{sse: !*noSSE, apiKey: *apiKey != ""})

	if *probeInterval > 0 {
		go probeLoop(ctx, svc, logger, *probeInterval)
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

// openCache 按参数选择缓存后端，返回后端名（用于日志）与实例。
//
// memory 走进程内实现，本地开发与单实例足够；redis:// 连接串走
// 分布式缓存，多实例共享一份。连不上 Redis 时在这里就报错退出，
// 不静默降级：降级会让"Redis 配错了"变成"缓存好像没生效"。
func openCache(spec string) (string, cache.Store, error) {
	if spec == "" || strings.EqualFold(spec, "memory") {
		return "memory", cache.NewMemory(), nil
	}
	lower := strings.ToLower(spec)
	if !strings.HasPrefix(lower, "redis://") && !strings.HasPrefix(lower, "rediss://") {
		return "", nil, fmt.Errorf("不支持的缓存后端 %q（用 memory 或 redis://…）", spec)
	}
	r, err := cache.NewRedis(context.Background(), cache.RedisOptions{URL: spec})
	if err != nil {
		return "", nil, err
	}
	return "redis", r, nil
}

// probeLoop 周期探活。
//
// 它的作用是让"下游恢复了"这件事自动生效——否则用户必须手动
// 触发刷新，而大多数人不会去点那个按钮。
func probeLoop(ctx context.Context, svc *hub.Service, logger *zap.Logger, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			report := svc.RefreshAll(ctx)
			if report.Failed > 0 {
				logger.Warn("周期探活存在失败节点",
					zap.Int("healthy", report.Healthy), zap.Int("failed", report.Failed))
			}
		}
	}
}

// waitForAddr 等 HTTP 服务真正 bind 后返回可用的基础 URL。
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
		if addr != "" && !isUnbound(addr) {
			return normalizeBase(addr)
		}
		time.Sleep(10 * time.Millisecond)
	}
	return ""
}

// isUnbound 判断地址是否还是配置里那个未绑定的形态。
//
// 两种形态都要认：`:9090`（Start 之前 Addr() 返回配置值，以冒号开头）
// 与 `127.0.0.1:0`（端口写 0，真实端口由系统分配）。只认前者的话，
// `--addr 127.0.0.1:0` 会打印出 http://127.0.0.1:0 —— 一个点不开的地址，
// 而"端口写 0 由系统分配"是 CLI 明确支持的用法。
func isUnbound(addr string) bool {
	if addr == "" {
		return true
	}
	if addr[0] == ':' {
		return true
	}
	_, port, err := net.SplitHostPort(addr)
	return err == nil && port == "0"
}

// normalizeBase 把监听地址折成可直接粘贴的 URL。
func normalizeBase(addr string) string {
	if len(addr) > 0 && addr[0] == ':' {
		return "http://127.0.0.1" + addr
	}
	// [::]:9090 → 127.0.0.1:9090
	if len(addr) > 4 && addr[:5] == "[::]:" {
		return "http://127.0.0.1:" + addr[5:]
	}
	if len(addr) > 0 && addr[0] == '[' {
		return "http://" + addr
	}
	return "http://" + addr
}

// printBanner 打印可直接复制粘贴的接入配置。
//
// 这一步是产品的"最后一厘米"：用户不需要读文档就知道该往
// 客户端配置里写什么。
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

	if conflicts := snap.Conflicts(); len(conflicts) > 0 {
		fmt.Printf("\n  警告：发现 %d 处工具命名冲突（已保留先到者）\n", len(conflicts))
		for _, c := range conflicts {
			fmt.Printf("    - %s\n", c)
		}
	}

	fmt.Printf("\n  客户端接入配置:\n")
	fmt.Printf("  { \"mcpServers\": { \"mcp-hub\": { \"url\": \"%s/mcp\" } } }\n\n", base)

	if opts.apiKey {
		fmt.Printf("  管理接口已开启鉴权，请求需带 X-API-Key 头。\n\n")
	}
}

// bannerOptions 描述启动横幅里那些"取决于怎么启动"的部分。
type bannerOptions struct {
	sse    bool
	apiKey bool
}

// isLoopbackAddr 判断监听地址是否只对本机开放。
//
// 只写端口（":9090"）等价于监听全部网卡——这一点常被误解成"本机"。
// 判错的方向必须偏保守：把暴露当成未暴露，代价是配置麻烦一点；
// 反过来是一台可被任意改配置的网关挂在网上。
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

// newLogger 构造带级别控制的 JSON 结构化日志。
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
