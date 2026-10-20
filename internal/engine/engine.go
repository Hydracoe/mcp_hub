// Package engine 是网关的装配根：把配置、存储、缓存、业务服务与 HTTP
// 端点组装成一个可启动 / 可停止的实例。
//
// 它存在的理由是把"怎么跑"和"跑在什么形态里"分开：
//
//	cmd/mcp-hub           命令行 / 服务端形态 —— 起引擎，等信号，退出
//	cmd/mcp-hub-desktop   桌面形态         —— 起引擎，让窗口指过去，关窗时停
//
// 两个入口的差别只在"谁来驱动生命周期"和"前端资源从哪来"，
// 装配顺序、探活、优雅退出这些一律在这里，不重复第二遍。
//
// 本包是**装配层**：它可以 import 任何内部包，反过来任何内部包都不该
// import 它。业务代码（hub / httpd / api）不认识 engine，也不该认识。
package engine

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/hydra/mcp-hub/internal/cache"
	"github.com/hydra/mcp-hub/internal/config"
	"github.com/hydra/mcp-hub/internal/httpd"
	"github.com/hydra/mcp-hub/internal/hub"
	"github.com/hydra/mcp-hub/internal/hub/retrieval"
	"github.com/hydra/mcp-hub/internal/store"
	"github.com/hydra/mcp-hub/internal/webassets"

	"go.uber.org/zap"
)

// shutdownTimeout 是优雅关闭的等待上限。
//
// 关窗 / Ctrl-C 之后必须走完这条路径，否则引擎拉起的 stdio 子进程
// 会变成孤儿进程一直挂在系统里。
const shutdownTimeout = 10 * time.Second

// Options 是装配参数。
type Options struct {
	// Settings 是配置层给出的全部配置。
	Settings config.Settings

	// Logger 留空则用 Nop（不写任何日志）。
	Logger *zap.Logger

	// DataDir 是数据目录：SQLite 的默认库文件落在它下面。
	// 命令行入口给当前工作目录，桌面入口给系统的应用数据目录。
	// 非 SQLite 后端忽略本项。
	DataDir string

	// Addr 覆盖 Settings.Addr。桌面端用它把端口固定成 127.0.0.1:0
	// （由系统分配），避免和用户机器上别的服务撞端口。
	Addr string
}

// Info 是启动完成后可以对外展示的运行事实。
//
// 它刻意是值类型：命令行用它打启动横幅，桌面端用它把地址告诉窗口，
// 两边都只是"读"，不该再去戳引擎内部。
type Info struct {
	// Addr 是实际监听地址（端口为 0 时这里是系统分配后的真实端口）。
	Addr string
	// BaseURL 是可直接访问的根地址，如 http://127.0.0.1:52831。
	BaseURL string
	// MCPURL / HealthzURL / ReadyzURL / OverviewURL 是常用的几个端点。
	MCPURL      string
	HealthzURL  string
	ReadyzURL   string
	OverviewURL string

	// UIURL 是管理界面的入口地址。UIEmbedded 为 false 时它没有意义。
	UIURL string
	// UIEmbedded 表示这个二进制里带没带管理界面。
	//
	// 为 false 有两种情形，且都不算错误：产物没被 `make ui` 拷进来，
	// 或这次部署本来就不需要界面（纯 API / 库形态）。入口据此决定
	// 要不要在横幅里打这一行——而不是打一个点开就是 404 的地址。
	UIEmbedded bool

	// DBDialect 是实际使用的数据库后端（postgres / sqlite）。
	DBDialect string
	// DBLocator 是数据库位置的脱敏描述，可以直接打进日志。
	DBLocator string
	// CacheDialect 是实际使用的缓存后端（redis / memory）。
	CacheDialect string
	// CacheShared 表示缓存是否跨进程共享。false 时多实例部署会各说各话。
	CacheShared bool

	EnableSSE bool
	APIKeySet bool
	// Warnings 是配置层攒下的可疑项，启动时该提醒一次。
	Warnings []string
}

// Engine 是装配好的网关实例。
type Engine struct {
	settings config.Settings
	log      *zap.Logger

	st    *store.Store
	cache cache.Store
	svc   *hub.Service
	srv   *httpd.Server

	// uiStatic 是要兜底的管理界面（没内嵌产物时为 nil）。
	uiStatic http.Handler

	// ctx 管住所有后台协程（探活调度、向量对账、日志落库）。
	// Stop 时统一取消——桌面端"关窗即停"靠的就是这一条。
	ctx    context.Context
	cancel context.CancelFunc

	mu      sync.Mutex
	started bool
	stopped bool
	info    Info
	errCh   chan error
}

// New 装配引擎：打开数据库与缓存、构造业务服务、接上向量能力。
// 它不监听任何端口——真正的启动在 Start。
//
// 返回错误时，已经打开的资源会被关上，调用方不必自己收尾。
func New(opts Options) (*Engine, error) {
	logger := opts.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	settings := opts.Settings
	if opts.Addr != "" {
		settings.Addr = opts.Addr
	}

	ctx, cancel := context.WithCancel(context.Background())
	e := &Engine{settings: settings, log: logger, ctx: ctx, cancel: cancel}

	st, err := store.OpenWith(store.Options{
		Driver:  settings.ResolvedDBDriver(),
		DSN:     settings.DB,
		DataDir: opts.DataDir,
		Log:     nil, // SQL 日志默认关：错误已经通过返回值传出。
	})
	if err != nil {
		cancel()
		return nil, err
	}
	e.st = st

	e.info.DBDialect = st.Dialect().Name()
	e.info.DBLocator = describeDB(st.Dialect().Name(), settings.DB, opts.DataDir)
	e.info.CacheDialect = settings.ResolvedCacheDriver()
	e.info.CacheShared = e.info.CacheDialect == cache.DriverRedis
	e.info.EnableSSE = settings.EnableSSE
	e.info.APIKeySet = settings.APIKey != ""
	e.info.Warnings = settings.Warnings

	// 界面这块资源值不值得挂，判断放在装配层而不是 httpd：
	// httpd 只该知道"有没有一块静态资源要兜底"，至于"它从哪来、
	// 是真产物还是占位页"属于部署形态的事。
	//
	// 没产物时宁可什么都不挂：挂上去只会得到一张占位页，而 404 至少
	// 诚实地说"这里没有界面"。真正的诊断信息由入口的横幅给。
	e.uiStatic = nil
	if webassets.Available() {
		e.uiStatic = webassets.Handler()
		e.info.UIEmbedded = true
	}

	if !e.info.CacheShared {
		logger.Info("缓存后端不跨进程共享",
			zap.String("backend", e.info.CacheDialect),
			zap.String("note", "多实例部署时各实例的缓存互相看不见，需要共享缓存请改回 redis"))
	}

	c, err := cache.Open(ctx, cache.Options{
		Driver: settings.ResolvedCacheDriver(),
		Spec:   settings.Cache,
	})
	if err != nil {
		e.closeResources()
		return nil, err
	}
	e.cache = c

	e.svc = hub.NewServiceWithOptions(st, logger, hub.Options{
		Cache:           c,
		ListMode:        settings.ListMode,
		BlockAfterFails: settings.ProbeBlockAfter,
	})

	e.attachEmbedding()
	e.logServerCount()
	return e, nil
}

// attachEmbedding 接上向量检索，并在后台补齐向量。
//
// 没配 EMBED_API_KEY 时 Embedder() 为 nil，整条向量路径关闭，
// 检索降级为 bm25——网关照常运行，这是刻意的降级阶梯。
func (e *Engine) attachEmbedding() {
	em := retrieval.NewEmbedManager(e.st, e.log, retrieval.EmbedOptions{
		BaseURL:       e.settings.EmbedBaseURL,
		APIKey:        e.settings.EmbedAPIKey,
		Model:         e.settings.EmbedModel,
		Dimensions:    e.settings.EmbedDims,
		Batch:         e.settings.EmbedBatch,
		CorpusFields:  e.settings.CorpusFields,
		RerankModel:   e.settings.RerankModel,
		RerankBaseURL: e.settings.RerankBaseURL,
		RerankAPIKey:  e.settings.RerankAPIKey,
	})
	e.svc.AttachEmbedding(em)
	if em.Embedder() == nil {
		return
	}

	// 后台补齐：向量重建可能要调几百次接口，绝不能挡在启动路径上。
	go func() {
		st := em.Reconcile(e.ctx)
		fields := []zap.Field{
			zap.String("state", string(st.State)),
			zap.Int("total", st.Total),
			zap.Int("done", st.Done),
			zap.Int("failed", st.Failed),
			zap.Int("covered", st.Covered),
			zap.String("message", st.Message),
		}
		if st.State == retrieval.RebuildSkipped {
			// 已有重建在跑：对账这次没做事，不等于"没就绪"。
			e.log.Info("向量对账跳过，已有向量重建在进行中", fields...)
			return
		}
		if st.Ready {
			e.log.Info("向量对账完成", fields...)
			return
		}
		e.log.Warn("向量对账未完全就绪，检索暂时降级 bm25", fields...)
	}()
}

// logServerCount 启动时把"库里配了多少个上游"打出来。
// 一个都没配时这句日志就是"为什么工具是空的"的直接答案。
func (e *Engine) logServerCount() {
	servers, err := e.svc.Servers(e.ctx)
	if err != nil {
		return
	}
	enabled := 0
	for _, sv := range servers {
		if sv.Enabled {
			enabled++
		}
	}
	e.log.Info("已从数据库加载",
		zap.Int("servers", len(servers)), zap.Int("enabled", enabled))
}

// Start 开始对外服务并返回运行信息。
//
// 顺序上先开监听、再探活：业务服务在构造时已经从库里装好了上一次的工具
// 快照，所以端口一开就能给出（可能略旧的）工具列表；而"还在探活"这件事
// 由 /readyz 表达。反过来先探活再监听，会让 /readyz 在最需要它的那段时间里
// 根本连不上——桌面端尤其明显：窗口起来却拿不到任何响应。
func (e *Engine) Start() (Info, error) {
	e.mu.Lock()
	if e.stopped {
		e.mu.Unlock()
		return Info{}, errors.New("引擎已停止，不能重新启动")
	}
	if e.started {
		info := e.info
		e.mu.Unlock()
		return info, nil
	}
	e.started = true
	e.mu.Unlock()

	e.srv = httpd.New(e.svc, httpd.Options{
		Addr:      e.settings.Addr,
		Logger:    e.log,
		EnableSSE: e.settings.EnableSSE,
		APIKey:    e.settings.APIKey,
		GinMode:   e.settings.GinMode,
		Static:    e.uiStatic,
	})

	e.errCh = make(chan error, 1)
	go func() { e.errCh <- e.srv.Start() }()

	base, err := e.waitForAddr()
	if err != nil {
		return Info{}, err
	}

	if e.settings.APIKey == "" && !isLoopbackAddr(e.settings.Addr) {
		e.log.Warn("管理接口已监听在非本机地址，但未设置 MCPHUB_API_KEY",
			zap.String("addr", e.settings.Addr),
			zap.String("risk", "任何能访问该地址的人都可以增删下游 server"))
	}

	if e.settings.ProbeOnStart {
		report := e.svc.RefreshAll(e.ctx)
		e.log.Info("启动探活完成",
			zap.Int("servers", report.Total),
			zap.Int("healthy", report.Healthy),
			zap.Int("failed", report.Failed),
			zap.Int("tools", report.ToolNum),
			zap.Int64("elapsedMs", report.Elapsed.Milliseconds()),
		)
	}

	// 周期探活由 e.ctx 管住：Stop 会取消它。
	if e.settings.ProbeInterval > 0 {
		e.svc.StartProbeScheduler(e.ctx, e.settings.ProbeInterval)
	}

	e.mu.Lock()
	e.info.Addr = e.srv.Addr()
	e.info.BaseURL = base
	e.info.MCPURL = base + "/mcp"
	e.info.HealthzURL = base + "/healthz"
	e.info.ReadyzURL = base + "/readyz"
	e.info.OverviewURL = base + "/api/overview"
	if e.info.UIEmbedded {
		e.info.UIURL = base + "/"
	}
	info := e.info
	e.mu.Unlock()
	return info, nil
}

// waitForAddr 等 HTTP 服务 bind 完成后返回可直接访问的根地址。
func (e *Engine) waitForAddr() (string, error) {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-e.errCh:
			if err != nil {
				return "", fmt.Errorf("启动 HTTP 服务失败: %w", err)
			}
			return "", errors.New("HTTP 服务已退出")
		default:
		}

		if addr := e.srv.Addr(); addr != "" && !httpd.IsUnbound(addr) {
			return httpd.NormalizeBase(addr), nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return "", errors.New("等待 HTTP 服务监听超时")
}

// Errors 返回 HTTP 服务的异常退出通道。
//
// 命令行入口靠它实现"服务自己挂了就退出进程"；桌面入口可以忽略它，
// 让窗口继续显示错误——进程还在，界面才有机会告诉用户发生了什么。
func (e *Engine) Errors() <-chan error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.errCh
}

// Info 返回运行信息。Start 之前 Addr / BaseURL 为空。
func (e *Engine) Info() Info {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.info
}

// Service 暴露业务服务：桌面端要把它的方法直接绑定给前端时用。
func (e *Engine) Service() *hub.Service { return e.svc }

// Handler 返回 HTTP 根 handler。
//
// 桌面端用它把 /api、/mcp 反代到自己前面那层资产服务器上，
// 从而在 webview 里保持同源——前端不必改成绝对地址，也不必开 CORS。
func (e *Engine) Handler() http.Handler {
	e.mu.Lock()
	srv := e.srv
	e.mu.Unlock()
	if srv == nil {
		return nil
	}
	return srv.Handler()
}

// Stop 优雅停止：停 HTTP、取消后台协程、断开缓存与数据库。可重复调用。
//
// 顺序是有讲究的：先停 HTTP（不再接新请求），再取消后台协程
// （不再发起新的下游调用），最后关业务服务与连接。
// 业务服务关闭时会等 stdio 子进程退出——这一步不能省。
func (e *Engine) Stop(ctx context.Context) error {
	e.mu.Lock()
	if e.stopped {
		e.mu.Unlock()
		return nil
	}
	e.stopped = true
	srv := e.srv
	e.mu.Unlock()

	if ctx == nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
	}

	var errs []error
	if srv != nil {
		if err := srv.Shutdown(ctx); err != nil {
			errs = append(errs, fmt.Errorf("关闭 HTTP 服务失败: %w", err))
		}
	}
	e.cancel()
	errs = append(errs, e.closeResources())
	return errors.Join(errs...)
}

// closeResources 关掉业务服务、缓存、数据库。可重复调用。
func (e *Engine) closeResources() error {
	var errs []error
	if e.svc != nil {
		if err := e.svc.Close(); err != nil {
			errs = append(errs, fmt.Errorf("关闭业务服务失败: %w", err))
		}
		e.svc = nil
	}
	if e.cache != nil {
		if err := e.cache.Close(); err != nil {
			errs = append(errs, fmt.Errorf("关闭缓存失败: %w", err))
		}
		e.cache = nil
	}
	if e.st != nil {
		if err := e.st.Close(); err != nil {
			errs = append(errs, fmt.Errorf("关闭数据库失败: %w", err))
		}
		e.st = nil
	}
	return errors.Join(errs...)
}

// ShutdownTimeout 是优雅关闭的等待上限，供入口构造关闭 ctx。
const ShutdownTimeout = shutdownTimeout

// describeDB 把数据库位置折成可以安全打印的描述。
// PostgreSQL 的 DSN 里带密码，必须脱敏；SQLite 的文件路径可以直接露出来。
func describeDB(dialect, dsn, dataDir string) string {
	dsn = strings.TrimSpace(dsn)
	switch dialect {
	case store.DriverPostgres:
		if u, err := url.Parse(dsn); err == nil {
			return u.Redacted()
		}
		return "postgres://(连接串无法解析)"
	case store.DriverSQLite:
		if dsn == "" {
			return "sqlite:" + dataDir
		}
		return "sqlite:" + dsn
	default:
		return dialect
	}
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
