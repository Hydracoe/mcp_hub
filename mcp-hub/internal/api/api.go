// Package api 实现网关的管理后端：把 HTTP 请求翻译成对 hub.Service 的调用，
// 把结果与 error 翻译回 HTTP 状态码。
//
// 业务判断全部留在 hub 层；本层不持有任何跨请求状态。
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

// secretMask 是密钥回显时的占位符；提交时收到该值表示沿用原值（见 mergeSecrets）。
const secretMask = "***"

// Options 是管理 API 的构造参数。
type Options struct {
	// APIKey 非空时，全部 /api/* 请求都必须带 X-API-Key 或 Bearer 令牌。
	// 服务默认只绑 127.0.0.1；监听 0.0.0.0 时必须启用。
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
// 鉴权只在 /api 分组上实现一次，新增端点挂进该组即自动受保护。
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

	// 密钥池。gin 路由树里字面量段与参数段可共存且字面量优先，
	// "import" 不会被 ":cid" 吃掉。
	g.GET("/servers/:id/credentials", a.handleListCredentials)
	g.POST("/servers/:id/credentials", a.handleCreateCredential)
	g.POST("/servers/:id/credentials/import", a.handleImportCredentials)
	g.PUT("/servers/:id/credentials/:cid", a.handleUpdateCredential)
	g.DELETE("/servers/:id/credentials/:cid", a.handleDeleteCredential)
	g.POST("/servers/:id/credentials/:cid/enable", a.handleEnableCredential)
	g.POST("/servers/:id/credentials/:cid/disable", a.handleDisableCredential)
	g.POST("/servers/:id/credentials/:cid/test", a.handleTestCredential)
	g.POST("/servers/:id/credentials/:cid/reset-quota", a.handleResetCredentialQuota)

	// 工具检索
	g.GET("/tools/search", a.handleSearchTools)

	// 分类
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

	// 嵌入检索没有配置接口：model / 维度 / 服务地址来自环境变量，改配置需重启；
	// 向量重建进度只打在进程日志里。
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
// 必须用常量时间比较，避免按字节差异泄露密钥。
func authorized(r *http.Request, want string) bool {
	got := r.Header.Get("X-API-Key")
	if got == "" {
		got = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// errorResponse 是管理 API 的统一错误体；code 供界面分支处理，不必匹配文案。
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
		// 引用不存在的分类属于请求错误（400），否则会写出悬空引用。
		abortErr(c, http.StatusBadRequest, "invalid_request", err.Error())
	case errors.As(err, &noKey):
		// 密钥此刻全部不可用：503 + Retry-After，客户端可原样重试。
		c.Header("Retry-After", strconv.Itoa(int(noKey.RetryAfter.Seconds())))
		abortErr(c, http.StatusServiceUnavailable, "no_usable_key", err.Error())
	case errors.As(err, &reqErr):
		abortErr(c, http.StatusBadRequest, "invalid_request", reqErr.msg)
	case errors.As(err, &unavail):
		// 下游处于退避窗口：503，客户端可稍后原样重试。
		abortErr(c, http.StatusServiceUnavailable, "unavailable", err.Error())
	default:
		abortErr(c, http.StatusInternalServerError, "internal", err.Error())
	}
}

// bindJSON 解析请求体并拒绝畸形输入。
// 必须 DisallowUnknownFields：字段名拼错时静默忽略会表现为保存成功但配置未生效。
// 请求体上限 1 MiB。
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
