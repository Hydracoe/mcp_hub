package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/hydra/mcp-hub/internal/store"
)

// categoryItem 是分类列表一行。
type categoryItem struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	ServerCount int    `json:"serverCount"`
}

func toCategoryItem(c store.Category) categoryItem {
	return categoryItem{
		ID:          c.ID,
		Name:        c.Name,
		Description: c.Description,
		ServerCount: c.ServerCount,
	}
}

type categoryRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// categoryNames 一次读出分类的 id→name 映射，供列表/详情展示分类名。
// 读失败时返回空表：分类名只是展示字段，不该让整个 server 列表 500。
func (a *API) categoryNames(ctx context.Context) map[string]string {
	out := map[string]string{}
	cats, err := a.svc.Categories(ctx)
	if err != nil {
		return out
	}
	for _, c := range cats {
		out[c.ID] = c.Name
	}
	return out
}

func (a *API) handleListCategories(c *gin.Context) {
	cats, err := a.svc.Categories(c.Request.Context())
	if err != nil {
		failFor(c, err)
		return
	}
	items := make([]categoryItem, 0, len(cats))
	for _, cat := range cats {
		items = append(items, toCategoryItem(cat))
	}
	c.JSON(http.StatusOK, map[string]any{"total": len(items), "items": items})
}

func (a *API) handleCreateCategory(c *gin.Context) {
	var req categoryRequest
	if err := bindJSON(c, &req); err != nil {
		failFor(c, err)
		return
	}
	cat, err := a.svc.CreateCategory(c.Request.Context(), req.Name, req.Description)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrCategoryTaken):
			abortErr(c, http.StatusConflict, "category_taken", "分类名已存在")
		default:
			failFor(c, err)
		}
		return
	}
	c.JSON(http.StatusCreated, toCategoryItem(*cat))
}

func (a *API) handleUpdateCategory(c *gin.Context) {
	var req categoryRequest
	if err := bindJSON(c, &req); err != nil {
		failFor(c, err)
		return
	}
	cat, err := a.svc.UpdateCategory(c.Request.Context(), c.Param("id"), req.Name, req.Description)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrCategoryTaken):
			abortErr(c, http.StatusConflict, "category_taken", "分类名已被其他分类占用")
		default:
			failFor(c, err)
		}
		return
	}
	c.JSON(http.StatusOK, toCategoryItem(*cat))
}

func (a *API) handleDeleteCategory(c *gin.Context) {
	if err := a.svc.DeleteCategory(c.Request.Context(), c.Param("id")); err != nil {
		failFor(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
