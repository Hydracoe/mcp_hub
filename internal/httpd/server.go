// Package httpd 提供网关的对外 HTTP 接口。
//
// 两套端点：MCP 协议端点给 AI 客户端用，管理端点给界面用。
// 前者是产品的全部价值所在，后者的每一次变更最终都要反映到前者。
//
// 路由与中间件建立在 gin 上；MCP 协议端点仍由 mcp-go 提供的
// http.Handler 直接接管，经 gin.WrapH 接入路由树——协议实现不该
// 为了换一个路由库而重写。
package httpd

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/hydra/mcp-hub/internal/api"
	"github.com/hydra/mcp-hub/internal/hub"
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
	//
	// 默认关闭：服务默认只绑 127.0.0.1。但一旦要监听到 0.0.0.0，
	// 这个开关必须现成可用——"先暴露出去、鉴权以后再加"从来
	// 都不是一个好计划。
	APIKey string
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

// New 构造 HTTP 服务但还不开始监听。
func New(svc *hub.Service, opts Options) *Server {
	if opts.Logger == nil {
		opts.Logger, _ = zap.NewProduction()
	}
	if opts.Addr == "" {
		opts.Addr = ":9090"
	}

	// 默认走 release 模式。开发模式每次启动都会往 stdout 打一段
	// 中间件提示，而这个进程的 stdout 是要留给用户看启动横幅的。
	// 需要调试时设 GIN_MODE=debug 即可，不额外加开关。
	if os.Getenv(gin.EnvGinMode) == "" {
		gin.SetMode(gin.ReleaseMode)
	}

	mcpHandler := mcpserver.NewStreamableHTTPServer(
		svc.Aggregator().MCP(),
		// 2026-07-28 起协议无状态，不需要粘性会话与共享 session store。
		// 这是网关能保持极低额外延迟的前提之一。
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

	// 把 gin 的路由行为调回标准库 mux 的语义，三处都改默认值：
	//   - 尾斜杠不重定向：gin 会把 /api/servers/ 301 到 /api/servers，
	//     而原先 mux 没有注册该模式，是 404。多一个 301 会让部分
	//     客户端把 GET 之外的方法丢掉。
	//   - 不做路径纠错：/api/server 这种近似拼写不该被悄悄猜中。
	//   - 方法不匹配回 405 而不是 404。
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

// Handler 返回装配好中间件的根 handler。
//
// 供测试用 httptest 直接挂载——不必真的占一个端口。
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

// --- 处理器 ---

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
	Ready     bool             `json:"ready"`
	ToolCount int              `json:"toolCount"`
	Servers   []hub.ServerView `json:"servers"`
	Conflicts []string         `json:"conflicts,omitempty"`
	BuiltAt   int64            `json:"builtAt"`
}

func (s *Server) handleStatus(c *gin.Context) {
	snap := s.svc.Registry().Current()
	ready, _ := s.svc.Ready()

	c.JSON(http.StatusOK, StatusResponse{
		Ready:     ready,
		ToolCount: snap.ToolCount(),
		Servers:   snap.Servers(),
		Conflicts: snap.Conflicts(),
		BuiltAt:   snap.BuiltAt(),
	})
}

// --- 中间件 ---

// mcpAuth 校验外部 AI 客户端的接入 API key。
//
// 策略是渐进收紧：库里没有任何启用中的 key 时放行（本地开发、
// 现有部署零配置不受影响）；一旦创建过 key，匿名请求一律 401——
// "创建第一把 key"这个动作本身就是启用鉴权的开关。
//
// 通过校验后把 key id 写进 request ctx，聚合器落调用日志时能归因
// 到具体调用方。每请求两次小查询（计数 + hash 查找），SQLite 本地
// 读，不是热点。
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

		c.Request = c.Request.WithContext(hub.WithCallerAPIKey(c.Request.Context(), key.ID))
		c.Next()
	}
}

// recover 兜住处理器里的 panic。
//
// 网关聚合的是第三方 server，其 schema 与注解完全不可控。
// 任何一处 panic 都只能影响这一个请求。
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

// cors 允许本地管理界面跨源访问。
//
// 生产形态里前端由 go:embed 内嵌，与后端同源，不需要 CORS；
// 但 Vite 开发服务器是另一个端口，缺了这段就没法联调。
func (s *Server) cors() gin.HandlerFunc {
	return func(c *gin.Context) {
		if origin := c.GetHeader("Origin"); origin != "" {
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
