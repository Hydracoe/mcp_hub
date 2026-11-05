// apikeys.go 实现接入 API key 的管理端点。
//
// 接入 key 的明文只在创建时返回一次，之后任何接口都只能看到前缀；
// 管理端点自身的鉴权复用 /api 分组的 guard（opts.APIKey），不与接入 key 混用。
package api

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/hydra/mcp-hub/internal/store/apikeys"
	"go.uber.org/zap"
)

// apiKeyItem 是列表里的一把接入 key，刻意不含 keyHash。
//
// 时间统一出 Unix 毫秒，与前端其余接口一致。
type apikeyItem struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Prefix  string `json:"prefix"`
	Enabled bool   `json:"enabled"`
	// LastUsedAt 为 0 表示从未被使用过，界面不应按 1970 年渲染。
	LastUsedAt int64 `json:"lastUsedAt"`
	CreatedAt  int64 `json:"createdAt"`
}

func toAPIKeyItem(k apikeys.APIKey) apikeyItem {
	item := apikeyItem{
		ID:        k.ID,
		Name:      k.Name,
		Prefix:    k.Prefix,
		Enabled:   k.Enabled,
		CreatedAt: k.CreatedAt.UnixMilli(),
	}
	if !k.LastUsedAt.IsZero() {
		item.LastUsedAt = k.LastUsedAt.UnixMilli()
	}
	return item
}

// apikeyCreateResponse 在创建响应里交付明文。
// 明文不入库，之后无法找回，界面必须提示用户立即保存。
type apikeyCreateResponse struct {
	apikeyItem
	Key string `json:"key"`
}

// handleCreateAPIKey 生成一把新接入 key。
func (a *API) handleCreateAPIKey(c *gin.Context) {
	var req struct {
		Name string `json:"name"`
	}
	if err := bindJSON(c, &req); err != nil {
		failFor(c, err)
		return
	}

	key, plaintext, err := a.svc.Store().APIKeys.Create(c.Request.Context(), req.Name)
	if err != nil {
		if errors.Is(err, apikeys.ErrAPIKeyNameRequired) {
			abortErr(c, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		failFor(c, err)
		return
	}

	a.log.Info("创建接入 API key", zap.String("name", key.Name), zap.String("id", key.ID))
	c.JSON(http.StatusCreated, apikeyCreateResponse{
		apikeyItem: toAPIKeyItem(key),
		Key:        plaintext,
	})
}

// handleListAPIKeys 返回全部接入 key（不含明文与 hash）。
func (a *API) handleListAPIKeys(c *gin.Context) {
	keys, err := a.svc.Store().APIKeys.List(c.Request.Context())
	if err != nil {
		failFor(c, err)
		return
	}
	items := make([]apikeyItem, 0, len(keys))
	for _, k := range keys {
		items = append(items, toAPIKeyItem(k))
	}
	c.JSON(http.StatusOK, gin.H{"total": len(items), "items": items})
}

// handleDeleteAPIKey 删除一把接入 key，立即生效：
// 后续用它访问 /mcp 的请求都会拿到 401。
func (a *API) handleDeleteAPIKey(c *gin.Context) {
	if err := a.svc.Store().APIKeys.Delete(c.Request.Context(), c.Param("id")); err != nil {
		failFor(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// handleEnableAPIKey / handleDisableAPIKey 停用而不删除，
// 临时收回权限时保留记录以便追溯。
func (a *API) handleEnableAPIKey(enabled bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := a.svc.Store().APIKeys.SetEnabled(c.Request.Context(), c.Param("id"), enabled); err != nil {
			failFor(c, err)
			return
		}
		c.Status(http.StatusNoContent)
	}
}
