package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/hydra/mcp-hub/internal/store/categories"
)

// categoryItem 是分类列表一行。
// name 用于展示，slug 进入对外工具名的前缀段。
type categoryItem struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Slug        string `json:"slug"`
	Description string `json:"description,omitempty"`
	ServerCount int    `json:"serverCount"`
	ToolCount   int    `json:"toolCount"`
}

func toCategoryItem(c categories.Category) categoryItem {
	return categoryItem{
		ID:          c.ID,
		Name:        c.Name,
		Slug:        c.Slug,
		Description: c.Description,
		ServerCount: c.ServerCount,
		ToolCount:   c.ToolCount,
	}
}

type categoryRequest struct {
	Name        string `json:"name"` // 中文名，界面展示
	Slug        string `json:"slug"` // 英文名，进对外工具名的前缀段
	Description string `json:"description"`
}

// categoryNames 一次读出分类的 id→name 映射；读失败返回空表，
// 因为分类名只是展示字段，不该让整个 server 列表 500。
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
	cat, err := a.svc.CreateCategory(c.Request.Context(), req.Name, req.Slug, req.Description)
	if err != nil {
		failForCategory(c, err)
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
	cat, err := a.svc.UpdateCategory(c.Request.Context(), c.Param("id"), req.Name, req.Slug, req.Description)
	if err != nil {
		failForCategory(c, err)
		return
	}
	c.JSON(http.StatusOK, toCategoryItem(*cat))
}

// failForCategory 翻译分类专有的领域错误。
// 中文名/英文名冲突与分类占用为 409，英文名不合规与中文名为空为 400，
// 其余交给 failFor。
func failForCategory(c *gin.Context, err error) {
	switch {
	case errors.Is(err, categories.ErrCategoryTaken):
		abortErr(c, http.StatusConflict, "category_taken", "分类中文名已被占用")
	case errors.Is(err, categories.ErrCategorySlugTaken):
		abortErr(c, http.StatusConflict, "category_taken", "分类英文名已被占用")
	case errors.Is(err, categories.ErrCategoryNameRequired):
		abortErr(c, http.StatusBadRequest, "invalid_request", "中文名不能为空")
	case errors.Is(err, categories.ErrCategorySlugInvalid):
		abortErr(c, http.StatusBadRequest, "invalid_request",
			"英文名不能为空，只能用 a-z、0-9 和单个连字符，且不能以连字符开头或结尾")
	case errors.Is(err, categories.ErrCategoryInUse):
		abortErr(c, http.StatusConflict, "category_in_use",
			"分类下还有 server，不能删除；请先把这些 server 改归其他分类或删除")
	default:
		failFor(c, err)
	}
}

func (a *API) handleDeleteCategory(c *gin.Context) {
	if err := a.svc.DeleteCategory(c.Request.Context(), c.Param("id")); err != nil {
		failForCategory(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
