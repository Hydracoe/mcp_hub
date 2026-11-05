package api

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/hydra/mcp-hub/internal/hub/retrieval"
)

// handleSearchTools 是管理界面的检索入口（PRD §4.7.7）。
//
// AI 客户端走 MCP meta-tool hub__search_tools，人走这个 HTTP 端点，
// 两条路共用同一个 SearchTools 实现，看到的必须是同一份结果。
func (a *API) handleSearchTools(c *gin.Context) {
	q := c.Request.URL.Query()

	var k int
	if raw := q.Get("k"); raw != "" {
		var err error
		if k, err = strconv.Atoi(raw); err != nil || k <= 0 {
			failFor(c, badRequest("k 需为正整数"))
			return
		}
	}

	hits, err := a.svc.SearchTools(c.Request.Context(), retrieval.SearchQuery{
		Query:      q.Get("query"),
		Mode:       q.Get("mode"),
		Category:   q.Get("category"),
		Server:     q.Get("server"),
		K:          k,
		WithSchema: q.Get("with_schema") == "1" || q.Get("with_schema") == "true",
	})
	if err != nil {
		failFor(c, err)
		return
	}
	c.JSON(http.StatusOK, map[string]any{
		"count":   len(hits),
		"facets":  retrieval.FacetsOf(hits),
		"results": hits,
	})
}
