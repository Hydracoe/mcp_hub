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
	//
	// 默认关闭：服务默认只绑 127.0.0.1。但一旦要监听到 0.0.0.0，
	// 这个开关必须现成可用——"先暴露出去、鉴权以后再加"从来
	// 都不是一个好计划。
	APIKey string

	// GinMode 是 HTTP 框架（gin）的运行模式：debug / release / test。
	//
	// 默认 release：开发模式每次启动都会往 stdout 打一段中间件提示，
	// 而这个进程的 stdout 要留给用户看启动横幅。
	// 值由配置层给（MCPHUB_GIN_MODE，gin 自己的 GIN_MODE 同义），
	// 这里不再自己读环境变量。
	GinMode string

	// Static 是管理界面的静态资源处理器，挂在路由表最后作为兜底。
	//
	// 这是"把界面内嵌进二进制"的挂载点：给一个 SPA 处理器，
	// 未命中 /api、/mcp、/healthz 的请求就落到界面。为 nil 时完全不挂，
	// 这些路径得到干净的 404——"没带界面"本身就是一种合法形态
	// （纯 API 部署、库形态、还没跑 `make ui`）。
	//
	// 路由本包不关心它从哪来（go:embed / 磁盘 / 内存），只关心
	// 它绝不会抢在具体路由前面——见 routes() 里的说明。
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
//
// 开发模式每次启动都会往 stdout 打一段中间件提示，而这个进程的 stdout
// 要留给用户看启动横幅，所以"没配"必须是 release 而不是 gin 自己的 debug。
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

	// 模式由调用方（配置层）决定；没给就按 release 算，
	// 保持"启动横幅是 stdout 上唯一输出"这条约定。
	gin.SetMode(resolveGinMode(opts.GinMode))

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

	// 静态界面必须是**最后**一块拼图，用 gin 的 NoRoute 而不是通配路由。
	//
	// 三点原因，缺一不可：
	//   - 顺序：它不认识路径语义，只认识文件。若挂在前面（或注册成
	//     "/" 通配），/api/servers 这类请求会先落到它手里，然后得到一个
	//     index.html —— 前端拿到 HTML 当 JSON 解，报错信息会指向完全
	//     无关的地方。
	//   - 不能用 catch-all：gin 的路由树不允许 `/*any` 与 `/healthz`、
	//     /api 这些根级路由共存（注册时直接 panic）。NoRoute 是唯一
	//     能兜底又不抢路由的位置。
	//   - 语义：NoRoute 只在"路由树里确实没有这条路由"时才触发。
	//     它天然不参与方法匹配，所以 POST /api/servers 仍然得到 405，
	//     而不是被 SPA 回退吞掉。
	if s.opts.Static != nil {
		s.engine.NoRoute(func(c *gin.Context) {
			// 保留命名空间下的未命中不归界面管。
			//
			// 这是 NoRoute 兜底最容易出问题的地方：/api/does-not-exist
			// 在路由树里没有对应节点，于是会落到这里。若回退成 index.html，
			// 客户端拿到的是 200 + HTML——它会先尝试解析、失败，
			// 然后报一个指向 JSON 解析器的错误，而真正的事实是"这个接口
			// 不存在"。200 还让"拼错了路径"在监控里完全隐形。
			//
			// 这里什么都不写就返回：gin 的 serveError 会补上它自己的
			// 默认 404，与"没挂界面"时的响应完全一致。
			if reservedPath(c.Request.URL.Path) {
				return
			}

			// 重置状态码是必须的，不是防御性代码。
			//
			// gin 进入 NoRoute 的路径是 serveError(c, 404, ...)：它**先**
			// 把 writermem.status 预置成 404，再调 c.Next()。而 gin 的
			// responseWriter 只在"尚未写响应"时才接受新的状态码——
			// 静态处理器若只是 set 头 + Write（http.ServeFile 之外最常见的
			// 写法，我们自己的 SPA 回退就是这样）就会带着 404 发出，
			// 于是"刷新页面"变成"页面有内容却仍是 404"，浏览器按错误处理，
			// 问题还更难查。
			//
			// 改回 200 只是把默认值复原；静态处理器随后可以用 WriteHeader
			// 覆盖成任何它想要的状态（未命中文件时 404）。
			c.Status(http.StatusOK)
			s.opts.Static.ServeHTTP(c.Writer, c.Request)
		})
	}
}

// reservedPath 报告某个路径是否属于"接口命名空间"，界面不该接管它。
//
// 判断用整段匹配而不是前缀字符串比较："/apifoo" 不是 /api 的子路径，
// 它应该照常回退到界面（前端确实可能有一个这样的路由）。
// 只有 "/api" 本身和 "/api/..." 才算。
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

// --- 中间件 ---

// mcpAuth 校验外部 AI 客户端的接入 API key。
//
// 策略是渐进收紧：库里一把 key 都没有时放行（本地开发、现有部署
// 零配置不受影响）；一旦创建过 key，匿名请求一律 401——"创建第一把
// key"这个动作本身就是启用鉴权的开关。
//
// 判断依据是"存在"而不是"启用中"（见 hub.APIKeysRequired）：全部停用
// 意味着把端点锁死，而不是退回匿名开放——停用是收紧，不是关闭鉴权。
//
// 通过校验后把 key id 写进 request ctx，聚合器落调用日志时能归因
// 到具体调用方。每请求两次小查询（计数 + hash 查找），PostgreSQL 本地
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

		// 名字一并注入：鉴权这里已经拿到完整的 apikeys.APIKey，
		// 落日志时就不必再查一次库（也避免把历史改写成"现在"的名字）。
		c.Request = c.Request.WithContext(callctx.WithCaller(c.Request.Context(), key.ID, key.Name))
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

// cors 只对本地来源开放跨源访问。
//
// 生产形态里前端由 go:embed 内嵌，与后端同源，不需要 CORS；Vite 开发
// 服务器是另一个端口，需要联调。但反射任意 Origin 会让用户浏览的任意
// 网页都能读取本机管理 API 的响应——默认监听回环地址也挡不住这种请求。
// 因此只回显 localhost / 127.0.0.1 / ::1 的来源，其余来源不拿 CORS 头。
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
