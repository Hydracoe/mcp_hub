package api

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/hydra/mcp-hub/internal/store/toolchanges"
)

// toolChangeItem 是一条工具变更记录的对外形态。
type toolChangeItem struct {
	ID        string    `json:"id"`
	ServerID  string    `json:"serverId"`
	ServerKey string    `json:"serverKey"`
	ToolName  string    `json:"toolName"`
	Action    string    `json:"action"`
	Detail    string    `json:"detail,omitempty"`
	Actor     string    `json:"actor"`
	TS        time.Time `json:"ts"`
}

// handleSetToolEnabled 处理单工具启用 / 禁用。
// 响应回整个 server 详情：界面拨完开关要立刻刷新这一屏的统计，
// 没必要再发一个 GET。
func (a *API) handleSetToolEnabled(enabled bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()
		id := c.Param("id")
		// 工具名按 MCP 规范只含字母数字 -_，路径段安全；不做额外转码。
		tool := c.Param("tool")

		if err := a.svc.SetToolEnabled(ctx, id, tool, enabled); err != nil {
			failFor(c, err)
			return
		}

		detail, err := a.detail(ctx, id)
		if err != nil {
			failFor(c, err)
			return
		}
		c.JSON(http.StatusOK, serverWriteResponse{serverDetail: detail})
	}
}

// handleListToolChanges 返回工具变更记录，可按 server 过滤。
func (a *API) handleListToolChanges(c *gin.Context) {
	q := c.Request.URL.Query()
	limit := queryInt(q.Get("limit"), 50)
	if limit > 200 {
		limit = 200
	}
	offset := queryInt(q.Get("offset"), 0)

	items, total, err := a.svc.ListToolChanges(c.Request.Context(), toolchanges.Query{
		ServerID: q.Get("serverId"),
		Limit:    limit,
		Offset:   offset,
	})
	if err != nil {
		failFor(c, err)
		return
	}

	out := make([]toolChangeItem, 0, len(items))
	for _, ch := range items {
		out = append(out, toolChangeItem{
			ID:        ch.ID,
			ServerID:  ch.ServerID,
			ServerKey: ch.ServerKey,
			ToolName:  ch.ToolName,
			Action:    ch.Action,
			Detail:    ch.Detail,
			Actor:     ch.Actor,
			TS:        ch.TS,
		})
	}
	c.JSON(http.StatusOK, gin.H{
		"total":  total,
		"limit":  limit,
		"offset": offset,
		"items":  out,
	})
}
