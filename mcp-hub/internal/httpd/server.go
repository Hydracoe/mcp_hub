// Package httpd 提供网关的对外 HTTP 接口：MCP 协议端点给 AI 客户端，管理端点给界面。
// 路由与中间件建立在 gin 上；MCP 协议端点由 mcp-go 的 http.Handler 直接接管，
// 经 gin.WrapH 接入路由树。
package httpd

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/hydra/mcp-hub/internal/api"
	"github.com/hydra/mcp-hub/internal/hub"
	"github.com/hydra/mcp-hub/internal/hub/callctx"
	"github.com/hydra/mcp-hub/internal/hub/registry"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"go.uber.org/zap"
)

// Options 是服务的启动参数。
type Options struct {
	Addr   string
	Logger *zap.Logger

	// EnableSSE 是否暴露旧版 SSE 端点。
	//
	// 新版协议用 Streamable HTTP，但现实里仍有客户端只认 SSE。
	// 成本很低，默认开启。
	EnableSSE bool

	// APIKey 非空时，管理接口要求请求带上它。
	// 服务默认只绑 127.0.0.1；监听 0.0.0.0 时必须启用。
	APIKey string

	// GinMode 是 gin 的运行模式：debug / release / test。
	// 默认 release：debug 会往 stdout 打中间件提示，而 stdout 要留给启动横幅。
	// 值由配置层给（MCPHUB_GIN_MODE，GIN_MODE 同义），这里不读环境变量。
	GinMode string

	// Static 是管理界面的静态资源处理器，作为 NoRoute 兜底挂在最后。
	// nil 时完全不挂，未命中路径得到干净的 404（纯 API 部署是合法形态）。
	// 路由本包不关心它从哪来，只保证它绝不会抢在具体路由前面（见 routes）。
	Static http.Handler
}

// Server 是网关的 HTTP 服务。
type Server struct {
	svc  *hub.Service
	log  *zap.Logger
	opts Options

	engine *gin.Engine
	http   *http.Server
	mcp    *mcpserver.StreamableHTTPServer
	sse    *mcpserver.SSEServer
	api    *api.API

	// boundAddr 在 Start 后保存实际监听地址。
	// 传 :0 时端口由系统分配，测试与"起一个看看"都需要真实端口。
	boundAddr atomic.Value
}

// resolveGinMode 把配置值落成 gin 认的模式名：留空按 release 算。
// release 保证启动横幅是 stdout 上唯一的输出。
func resolveGinMode(mode string) string {
	if mode == "" {
		return gin.ReleaseMode
	}
	return mode
}

// New 构造 HTTP 服务但还不开始监听。
func New(svc *hub.Service, opts Options) *Server {
	if opts.Logger == nil {
		opts.Logger, _ = zap.NewProduction()
	}
	if opts.Addr == "" {
		opts.Addr = "127.0.0.1:9090"
	}

	// 模式由调用方（配置层）决定；没给就按 release 算。
	gin.SetMode(resolveGinMode(opts.GinMode))

	mcpHandler := mcpserver.NewStreamableHTTPServer(
		svc.Aggregator().MCP(),
		// 协议无状态，不需要粘性会话与共享 session store；这是低延迟的前提之一。
		mcpserver.WithStateLess(true),
		mcpserver.WithEndpointPath("/mcp"),
	)

	s := &Server{
		svc:    svc,
		log:    opts.Logger,
		opts:   opts,
		engine: gin.New(),
		mcp:    mcpHandler,
		api: api.New(svc, api.Options{
			APIKey:    opts.APIKey,
			Logger:    opts.Logger,
			EnableSSE: opts.EnableSSE,
		}),
	}

	// 把 gin 路由行为调回标准库 mux 的语义：尾斜杠不重定向（否则部分客户端会丢掉
	// GET 之外的方法）、不做路径纠错（近似拼写不该被悄悄猜中）、方法不匹配回 405 而非 404。
	s.engine.RedirectTrailingSlash = false
	s.engine.RedirectFixedPath = false
	s.engine.HandleMethodNotAllowed = true

	// 中间件必须在注册路由之前挂载：gin 在注册时把当前中间件链
	// 快照进每条路由，晚挂的中间件不会补到已注册的路由上。
	s.engine.Use(s.recover(), s.accessLog(), s.cors())
	s.routes()

	s.http = &http.Server{
		Addr:              opts.Addr,
		Handler:           s.engine,
		ReadHeaderTimeout: 10 * time.Second,
		// 不设 WriteTimeout：SSE 与流式响应是长连接，
		// 一个全局写超时会把正常的长连接掐断。
		IdleTimeout: 120 * time.Second,
	}
	return s
}

// routes 注册全部端点。
func (s *Server) routes() {
	// MCP 端点的鉴权边界：配了启用中的接入 key 就强制校验，
	// 一把都没配则放行（本地零配置体验）。见 mcpAuth 的说明。
	mcpGuard := s.mcpAuth()

	// 无状态流式 HTTP：/mcp 与 /mcp/ 子树都交给同一个 handler，
	// 有些客户端会带尾斜杠。
	s.engine.Any("/mcp", mcpGuard, gin.WrapH(s.mcp))
	s.engine.Any("/mcp/*path", mcpGuard, gin.WrapH(s.mcp))

	if s.opts.EnableSSE {
		s.sse = mcpserver.NewSSEServer(s.svc.Aggregator().MCP())
		s.engine.Any("/sse", mcpGuard, gin.WrapH(s.sse.SSEHandler()))
		s.engine.Any("/message", mcpGuard, gin.WrapH(s.sse.MessageHandler()))
	}

	s.engine.GET("/healthz", s.handleHealthz)
	s.engine.GET("/readyz", s.handleReadyz)
	s.engine.GET("/api/status", s.handleStatus)

	// 管理接口挂在同一台引擎上：它们与 MCP 端点共享进程与状态，
	// 分开两个端口只会多一个需要配置、需要解释的东西。
	s.api.Register(s.engine)

	// 静态界面必须最后挂，用 NoRoute 而不是通配路由：NoRoute 只在路由树确实没有
	// 该路由时触发，不参与方法匹配，所以 POST /api/servers 仍是 405 而不是被 SPA 吞掉；
	// gin 路由树也不允许 /*any 与 /healthz、/api 等根级路由共存（注册即 panic）。
	if s.opts.Static != nil {
		s.engine.NoRoute(func(c *gin.Context) {
			// 保留命名空间下的未命中不归界面管：回退成 index.html 会让客户端拿到
			// 200 + HTML，把“接口不存在”伪装成解析错误，并在监控里隐形。
			if reservedPath(c.Request.URL.Path) {
				return
			}

			// 重置状态码是必须的：gin 进入 NoRoute 前已把 status 预置为 404，
			// 而 responseWriter 只在尚未写响应时接受新状态码；静态处理器若只 set 头 + Write
			// 就会带着 404 发出，表现为“页面有内容却仍是 404”。
			c.Status(http.StatusOK)
			s.opts.Static.ServeHTTP(c.Writer, c.Request)
		})
	}
}

// reservedPath 报告路径是否属于接口命名空间。
// 用整段匹配而非前缀字符串比较："/apifoo" 应照常回退到界面；
// 只有 "/api" 本身与 "/api/..." 才算（其余前缀同理）。
func reservedPath(p string) bool {
	for _, prefix := range []string{"/api", "/mcp", "/sse", "/message", "/healthz", "/readyz"} {
		if p == prefix || strings.HasPrefix(p, prefix+"/") {
			return true
		}
	}
	return false
}

// Start 开始监听并阻塞，直到服务器关闭。
func (s *Server) Start() error {
	ln, err := net.Listen("tcp", s.opts.Addr)
	if err != nil {
		return fmt.Errorf("监听 %s 失败: %w", s.opts.Addr, err)
	}

	s.boundAddr.Store(ln.Addr().String())
	s.log.Info("HTTP 服务已启动", zap.String("addr", ln.Addr().String()))

	if err := s.http.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Handler 返回装配好中间件的根 handler，供测试用 httptest 直接挂载。
func (s *Server) Handler() http.Handler { return s.http.Handler }

// Shutdown 优雅关闭：停止接收新请求，等待在途请求完成。
func (s *Server) Shutdown(ctx context.Context) error {
	return s.http.Shutdown(ctx)
}

// Addr 返回实际监听地址。Start 之前返回配置值。
func (s *Server) Addr() string {
	if addr, ok := s.boundAddr.Load().(string); ok && addr != "" {
		return addr
	}
	return s.opts.Addr
}

func (s *Server) handleHealthz(c *gin.Context) {
	// 存活探针只回答"进程还在吗"。任何依赖下游的判断都会让
	// 下游抖动演变成网关被重启。
	c.JSON(http.StatusOK, map[string]any{"status": "ok"})
}

func (s *Server) handleReadyz(c *gin.Context) {
	ready, unhealthy := s.svc.Ready()

	code := http.StatusOK
	status := "ready"
	if !ready {
		code = http.StatusServiceUnavailable
		status = "not_ready"
	}

	c.JSON(code, map[string]any{
		"status":         status,
		"toolCount":      s.svc.Registry().Current().ToolCount(),
		"unhealthyNodes": unhealthy,
	})
}

// StatusResponse 是管理界面用的聚合状态。
type StatusResponse struct {
	Ready     bool                  `json:"ready"`
	ToolCount int                   `json:"toolCount"`
	Servers   []registry.ServerView `json:"servers"`
	BuiltAt   int64                 `json:"builtAt"`
}

func (s *Server) handleStatus(c *gin.Context) {
	snap := s.svc.Registry().Current()
	ready, _ := s.svc.Ready()

	c.JSON(http.StatusOK, StatusResponse{
		Ready:     ready,
		ToolCount: snap.ToolCount(),
		Servers:   snap.Servers(),
		BuiltAt:   snap.BuiltAt(),
	})
}

// mcpAuth 校验外部 AI 客户端的接入 API key。
// 库里一把 key 都没有时放行；一旦存在 key，匿名请求一律 401（创建第一把即启用鉴权）。
// 判断依据是“存在”而非“启用中”（全停用=锁死）；校验通过把 key id/名字写进 ctx 供日志归因。
func (s *Server) mcpAuth() gin.HandlerFunc {
	present := func(c *gin.Context) string {
		if k := c.GetHeader("X-API-Key"); k != "" {
			return k
		}
		return strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
	}
	unauthorized := func(c *gin.Context) {
		c.Header("WWW-Authenticate", "Bearer")
		c.AbortWithStatusJSON(http.StatusUnauthorized, map[string]any{
			"code":  "unauthorized",
			"error": "缺少或无效的接入 API key：请在 X-API-Key 头或 Authorization: Bearer 中提供",
		})
	}

	return func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
		defer cancel()

		required, err := s.svc.APIKeysRequired(ctx)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusInternalServerError,
				map[string]any{"code": "internal", "error": "鉴权检查失败"})
			return
		}
		if !required {
			c.Next()
			return
		}

		key, ok, err := s.svc.VerifyAPIKey(ctx, present(c))
		if err != nil {
			c.AbortWithStatusJSON(http.StatusInternalServerError,
				map[string]any{"code": "internal", "error": "鉴权检查失败"})
			return
		}
		if !ok {
			unauthorized(c)
			return
		}

		// 名字一并注入：鉴权这里已拿到完整记录，落日志时不必再查库，
		// 也避免把历史改写成“现在”的名字。
		c.Request = c.Request.WithContext(callctx.WithCaller(c.Request.Context(), key.ID, key.Name))
		c.Next()
	}
}

// recover 兜住处理器里的 panic。
// 网关聚合第三方 server，其 schema 与注解不可控；一处 panic 只能影响这一个请求。
func (s *Server) recover() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if rec := recover(); rec != nil {
				s.log.Error("请求处理 panic",
					zap.String("path", c.Request.URL.Path),
					zap.String("method", c.Request.Method),
					zap.Any("recover", rec))
				c.AbortWithStatusJSON(http.StatusInternalServerError,
					map[string]any{"error": "internal error"})
			}
		}()
		c.Next()
	}
}

// cors 只对本地来源开放跨源访问。
// 反射任意 Origin 会让用户浏览的网页也能读本机管理 API；默认监听回环也挡不住。
// 只回显 localhost / 127.0.0.1 / ::1 的来源，其余来源不拿 CORS 头。
func (s *Server) cors() gin.HandlerFunc {
	return func(c *gin.Context) {
		if origin := c.GetHeader("Origin"); isLocalOrigin(origin) {
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Vary", "Origin")
			c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization, X-API-Key, Mcp-Session-Id, Mcp-Method, Mcp-Name, MCP-Protocol-Version")
			c.Header("Access-Control-Expose-Headers", "Mcp-Session-Id, MCP-Protocol-Version")
			c.Header("Access-Control-Max-Age", "600")
		}
		if c.Request.Method == http.MethodOptions {
			// 预检要在路由匹配之前结束：OPTIONS 不该落到业务处理器上。
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

// isLocalOrigin 判断 Origin 是否指向本机回环地址。
//
// 只接受 http/https：Origin 是 "null" 或带其他 scheme 的值一律拒绝。
func isLocalOrigin(origin string) bool {
	if origin == "" {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	switch strings.ToLower(u.Hostname()) {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return false
}

func (s *Server) accessLog() gin.HandlerFunc {
	return func(c *gin.Context) {
		path := c.Request.URL.Path

		// 健康检查每秒都被打，记进日志只会淹没真正的信息。
		if path == "/healthz" || path == "/readyz" {
			c.Next()
			return
		}

		started := time.Now()
		c.Next()

		// 状态码直接读 gin 的 writer：它自己就记录了 WriteHeader 的值，
		// 不必再包一层 ResponseWriter（包了还得手动实现 Flush 才能让 SSE 穿透）。
		s.log.Info("http",
			zap.String("method", c.Request.Method),
			zap.String("path", path),
			zap.Int("status", c.Writer.Status()),
			zap.Int64("ms", time.Since(started).Milliseconds()),
		)
	}
}
