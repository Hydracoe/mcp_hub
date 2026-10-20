// Package api 实现网关的管理后端。
//
// 这一层的职责只有翻译：把 HTTP 请求翻译成对 hub.Service 的一次调用，
// 把结果与 error 翻译回 HTTP 状态码。所有业务判断都留在 hub 层——
// 一旦这里开始自己算"什么算健康""要不要刷新缓存"，界面能做的事
// 与 CLI 能做的事就会慢慢长出两套语义，而它们迟早会打架。
//
// 路由与请求解析交给 gin，翻译层本身不持有任何跨请求状态。
package api

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/hydra/mcp-hub/internal/downstream"
	"github.com/hydra/mcp-hub/internal/hub"
	"github.com/hydra/mcp-hub/internal/hub/pool"
	"github.com/hydra/mcp-hub/internal/store/credentials"
	"github.com/hydra/mcp-hub/internal/store/servers"
	"github.com/hydra/mcp-hub/internal/store/storeutil"
	"go.uber.org/zap"
)

// secretMask 是密钥回显时的占位符。
//
// 提交时收到这个值表示"沿用原值"，见 mergeSecrets。
const secretMask = "***"

// Options 是管理 API 的构造参数。
type Options struct {
	// APIKey 非空时，全部 /api/* 请求都必须带 X-API-Key 或 Bearer 令牌。
	//
	// v1 默认关闭：服务默认只绑 127.0.0.1，本机访问没有中间人。
	// 但一旦要监听到 0.0.0.0，这个开关必须能用——事后补鉴权
	// 往往意味着已经暴露过了。
	APIKey string

	Logger *zap.Logger

	// EnableSSE 让接入指引里也带上旧版 SSE 端点。
	EnableSSE bool

	// StartedAt 用于概览里的运行时长。零值取构造时刻。
	StartedAt time.Time
}

// API 是管理端点的集合。
type API struct {
	svc  *hub.Service
	log  *zap.Logger
	opts Options
}

// New 构造管理 API。
func New(svc *hub.Service, opts Options) *API {
	if opts.Logger == nil {
		opts.Logger, _ = zap.NewProduction()
	}
	if opts.StartedAt.IsZero() {
		opts.StartedAt = time.Now()
	}
	return &API{svc: svc, log: opts.Logger, opts: opts}
}

// Register 把全部管理端点挂到路由上。
//
// 集中注册而不是散在各处：鉴权只有一处实现，也就不可能出现
// "新加的端点忘了加鉴权"这种最难在代码评审里发现的漏洞。
func (a *API) Register(r gin.IRouter) {
	// 分组即鉴权边界：组内每一条路由都自动带上 guard，
	// 新增端点只要挂进这个组，就不可能漏鉴权。
	g := r.Group("/api", a.guard())

	// 概览与查询
	g.GET("/overview", a.handleOverview)
	g.GET("/tools", a.handleTools)
	g.GET("/logs", a.handleLogs)
	g.DELETE("/logs", a.handleClearLogs)
	g.GET("/connect", a.handleConnect)

	// server 管理
	g.GET("/servers", a.handleListServers)
	g.POST("/servers", a.handleCreateServer)
	g.POST("/servers/test-all", a.handleTestAll)
	// 干跑探活：配置尚未落库，直接对请求体里的 Spec 试 L1–L3 + tools/list。
	// 字面量段与 ":id" 参数段共存，"probe" 不会被当成 id。
	g.POST("/servers/probe", a.handleProbeServer)
	g.GET("/servers/:id", a.handleGetServer)
	g.PUT("/servers/:id", a.handleUpdateServer)
	g.DELETE("/servers/:id", a.handleDeleteServer)
	g.POST("/servers/:id/enable", a.handleEnableServer(true))
	g.POST("/servers/:id/disable", a.handleEnableServer(false))
	g.POST("/servers/:id/test", a.handleTestServer)
	g.POST("/servers/:id/refresh-tools", a.handleRefreshTools)

	// 单工具启用 / 禁用与工具变更记录。
	g.POST("/servers/:id/tools/:tool/enable", a.handleSetToolEnabled(true))
	g.POST("/servers/:id/tools/:tool/disable", a.handleSetToolEnabled(false))
	g.GET("/tool-changes", a.handleListToolChanges)

	// 密钥池（M4）。gin 的路由树里字面量段与参数段可以共存，
	// 且字面量优先——"import" 不会被 ":cid" 吃掉。
	g.GET("/servers/:id/credentials", a.handleListCredentials)
	g.POST("/servers/:id/credentials", a.handleCreateCredential)
	g.POST("/servers/:id/credentials/import", a.handleImportCredentials)
	g.PUT("/servers/:id/credentials/:cid", a.handleUpdateCredential)
	g.DELETE("/servers/:id/credentials/:cid", a.handleDeleteCredential)
	g.POST("/servers/:id/credentials/:cid/enable", a.handleEnableCredential)
	g.POST("/servers/:id/credentials/:cid/disable", a.handleDisableCredential)
	g.POST("/servers/:id/credentials/:cid/test", a.handleTestCredential)
	g.POST("/servers/:id/credentials/:cid/reset-quota", a.handleResetCredentialQuota)

	// 工具检索（PRD §4.7）。
	g.GET("/tools/search", a.handleSearchTools)

	// 分类（PRD §4.7.3）。
	g.GET("/categories", a.handleListCategories)
	g.POST("/categories", a.handleCreateCategory)
	g.PUT("/categories/:id", a.handleUpdateCategory)
	g.DELETE("/categories/:id", a.handleDeleteCategory)

	// 接入 API key（外部 AI 客户端调用 /mcp 的凭据）。
	g.POST("/keys", a.handleCreateAPIKey)
	g.GET("/keys", a.handleListAPIKeys)
	g.DELETE("/keys/:id", a.handleDeleteAPIKey)
	g.POST("/keys/:id/enable", a.handleEnableAPIKey(true))
	g.POST("/keys/:id/disable", a.handleEnableAPIKey(false))

	// 嵌入检索（PRD §4.7.5）没有配置接口：model / 维度 / 服务地址都来自
	// 环境变量，改配置就是改 .env 重启。它也就没有"读配置"的必要——
	// 管理接口只暴露工具与分类这类运行时数据；向量重建的进度打在进程
	// 日志里（见 serve 的启动对账）。
}

// guard 是管理端点的统一入口检查。
func (a *API) guard() gin.HandlerFunc {
	return func(c *gin.Context) {
		if a.opts.APIKey != "" && !authorized(c.Request, a.opts.APIKey) {
			abortErr(c, http.StatusUnauthorized, "unauthorized",
				"缺少或错误的凭据：请在 X-API-Key 头或 Authorization: Bearer 中提供")
		}
	}
}

// authorized 校验请求携带的凭据。
//
// 用常量时间比较：普通字符串比较会在第一个不同的字节上返回，
// 让攻击者能逐字节猜出密钥。
func authorized(r *http.Request, want string) bool {
	got := r.Header.Get("X-API-Key")
	if got == "" {
		got = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// --- 响应辅助 ---

// errorResponse 是管理 API 的统一错误体。
//
// 带上机器可读的 code：界面据此区分"要弹表单校验"还是"要提示稍后重试"，
// 不必去匹配人话文案。
type errorResponse struct {
	Code  string `json:"code"`
	Error string `json:"error"`
}

// abortErr 写出错误体并中断后续处理器。
func abortErr(c *gin.Context, status int, code, msg string) {
	c.AbortWithStatusJSON(status, errorResponse{Code: code, Error: msg})
}

// requestError 表示请求体本身有问题，应当由调用方修正。
type requestError struct{ msg string }

func (e *requestError) Error() string { return e.msg }

func badRequest(format string, args ...any) error {
	return &requestError{msg: fmt.Sprintf(format, args...)}
}

// failFor 把领域错误翻译成 HTTP 状态码。
func failFor(c *gin.Context, err error) {
	var reqErr *requestError
	var unavail *pool.UnavailableError
	var noKey *downstream.ErrNoUsableKey
	var catMissing *hub.CategoryNotFoundError

	switch {
	case errors.Is(err, storeutil.ErrNotFound):
		abortErr(c, http.StatusNotFound, "not_found", "目标不存在")
	case errors.Is(err, servers.ErrKeyTaken):
		abortErr(c, http.StatusConflict, "key_taken", err.Error())
	case errors.Is(err, credentials.ErrCredentialExists):
		abortErr(c, http.StatusConflict, "credential_exists", err.Error())
	case errors.Is(err, hub.ErrDuplicateCredential):
		abortErr(c, http.StatusConflict, "credential_duplicate", err.Error())
	case errors.Is(err, hub.ErrKeyPoolUnsupported):
		abortErr(c, http.StatusBadRequest, "key_pool_unsupported", err.Error())
	case errors.Is(err, hub.ErrServerDisabled):
		abortErr(c, http.StatusBadRequest, "server_disabled", err.Error())
	case errors.As(err, &catMissing):
		// 引用了不存在的分类：请求写错了，不是服务端故障。
		// 写下去就成了悬空引用（界面上是一行空白分类），所以是 400。
		abortErr(c, http.StatusBadRequest, "invalid_request", err.Error())
	case errors.As(err, &noKey):
		// 所有密钥此刻都不可用。这是"暂时不行"，不是"请求错了"：
		// 503 + retryAfter 让客户端知道可以原样重试，而不是去改配置。
		c.Header("Retry-After", strconv.Itoa(int(noKey.RetryAfter.Seconds())))
		abortErr(c, http.StatusServiceUnavailable, "no_usable_key", err.Error())
	case errors.As(err, &reqErr):
		abortErr(c, http.StatusBadRequest, "invalid_request", reqErr.msg)
	case errors.As(err, &unavail):
		// 下游处于退避窗口。这是"暂时不行"，不是"请求错了"，
		// 503 让客户端知道可以稍后原样重试。
		abortErr(c, http.StatusServiceUnavailable, "unavailable", err.Error())
	default:
		abortErr(c, http.StatusInternalServerError, "internal", err.Error())
	}
}

// bindJSON 解析请求体，并拒绝明显的畸形输入。
//
// 不用 gin 自带的 ShouldBindJSON：它默认允许未知字段，而字段名拼错时
// 静默忽略，用户会看到"保存成功但配置没生效"，这比直接报错难查得多。
// 这里保留 DisallowUnknownFields 与 1 MiB 上限的原有语义。
func bindJSON(c *gin.Context, dst any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, c.Request.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return badRequest("请求体不是合法的 JSON: %v", err)
	}
	return nil
}

// maxBodyBytes 限制请求体大小。工具 schema 可能很大，配置不会。
const maxBodyBytes = 1 << 20 // 1 MiB

// parseBool 解析查询参数里的布尔值，缺省返回 def。
func parseBool(raw string, def bool) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return def
}
