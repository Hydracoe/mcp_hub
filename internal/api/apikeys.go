// apikeys.go 实现接入 API key 的管理端点。
//
// 接入 key 是外部 AI 客户端调用 /mcp 端点的凭据：创建时明文只返回
// 一次，之后任何接口都只能看到前缀。管理端点自身的鉴权复用 /api
// 分组上已有的 guard（opts.APIKey），不与接入 key 混用——管理凭据
// 与接入凭据的泄露后果完全不同，不该互相兜底。
package api

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/hydra/mcp-hub/internal/store/apikeys"
	"go.uber.org/zap"
)

// apiKeyItem 是列表里的一把接入 key。刻意不含 keyHash：
// 接口消费方不需要它，库里有 hash 就够了。
//
// 时间统一出 Unix 毫秒，与前端其余接口一致 —— 混用 RFC3339 与毫秒
// 意味着每个消费方都要各写一套解析，而其中一套迟早会写错。
type apikeyItem struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Prefix  string `json:"prefix"`
	Enabled bool   `json:"enabled"`
	// LastUsedAt 为 0 表示从未被使用过。界面据此显示「从未使用」，
	// 而不是把它当 1970 年渲染成「20000d ago」。
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
//
// key 字段只在这一次出现：明文没有入库，之后谁也找不回来。
// 界面必须提示用户立刻保存——这是不可重放的交付。
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

// handleEnableAPIKey / handleDisableAPIKey 停用而不删除：
// 临时收回一个调用方的权限时保留记录，比删了重建可追溯。
func (a *API) handleEnableAPIKey(enabled bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := a.svc.Store().APIKeys.SetEnabled(c.Request.Context(), c.Param("id"), enabled); err != nil {
			failFor(c, err)
			return
		}
		c.Status(http.StatusNoContent)
	}
}
