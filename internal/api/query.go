package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/hydra/mcp-hub/internal/downstream"
	"github.com/hydra/mcp-hub/internal/hub"
	"github.com/hydra/mcp-hub/internal/store"
	"go.uber.org/zap"
)

// statsWindow 是概览里"近期调用"的默认观察窗。
const statsWindow = 24 * time.Hour

// --- 概览 ---

type overviewResponse struct {
	Ready         bool  `json:"ready"`
	BuiltAt       int64 `json:"builtAt"`
	UptimeSeconds int64 `json:"uptimeSeconds"`

	Servers serverStats   `json:"servers"`
	Tools   toolStats     `json:"tools"`
	Calls   callStatsView `json:"calls"`

	// Credentials 只统计启用了密钥池的 server。为 0 表示全站在用
	// 单密钥模式（沿用 servers.headers），界面据此换一种呈现方式。
	Credentials hub.KeySummary `json:"credentials"`

	Conflicts []string          `json:"conflicts,omitempty"`
	Endpoints map[string]string `json:"endpoints"`
}

type serverStats struct {
	Total    int `json:"total"`
	Enabled  int `json:"enabled"`
	Healthy  int `json:"healthy"`
	Degraded int `json:"degraded"`
	Offline  int `json:"offline"`
	Unknown  int `json:"unknown"`
}

type toolStats struct {
	Total int `json:"total"`
}

// callStatsView 在原始统计上补一个失败率。
//
// 前端算也行，但"失败率"是个容易算错的东西（分母取总数还是
// 成功数），放在一处算比散在各处算更不容易出偏差。
type callStatsView struct {
	store.CallStats
	FailureRate float64 `json:"failureRate"`
}

func (a *API) handleOverview(c *gin.Context) {
	ctx := c.Request.Context()

	servers, err := a.svc.Servers(ctx)
	if err != nil {
		failFor(c, err)
		return
	}

	stats := serverStats{Total: len(servers)}
	for _, sv := range servers {
		if sv.Enabled {
			stats.Enabled++
		}
		switch sv.Status {
		case "healthy":
			stats.Healthy++
		case "degraded":
			stats.Degraded++
		case "offline":
			stats.Offline++
		default:
			stats.Unknown++
		}
	}

	// 走 Service 的缓存读口，而不是直接捅到 store：日志表会一直长，
	// 这个聚合扫描是概览里唯一随数据量变慢的查询。
	calls, err := a.svc.RecentStats(ctx, statsWindow)
	if err != nil {
		// 统计失败不该让整个概览打不开：把调用数留空，
		// 其余信息（server 状态、工具数）仍然有用。
		a.log.Error("统计调用情况失败", zap.Error(err))
	}
	callView := callStatsView{CallStats: calls}
	if calls.Total > 0 {
		callView.FailureRate = float64(calls.Failed) / float64(calls.Total)
	}

	snap := a.svc.Registry().Current()
	ready, _ := a.svc.Ready()

	keys, err := a.svc.KeySummary(ctx)
	if err != nil {
		// 与调用统计同样处理：密钥统计失败不该让概览整个打不开。
		a.log.Error("统计密钥状况失败", zap.Error(err))
	}

	c.JSON(http.StatusOK, overviewResponse{
		Ready:         ready,
		BuiltAt:       snap.BuiltAt(),
		UptimeSeconds: int64(time.Since(a.opts.StartedAt).Seconds()),
		Servers:       stats,
		Tools:         toolStats{Total: snap.ToolCount()},
		Calls:         callView,
		Credentials:   keys,
		Conflicts:     snap.Conflicts(),
		Endpoints:     endpoints(c.Request, a.opts.EnableSSE),
	})
}

// --- 工具浏览器 ---

func (a *API) handleTools(c *gin.Context) {
	q := c.Request.URL.Query()
	serverFilter := q.Get("serverId")
	if serverFilter == "" {
		serverFilter = q.Get("serverKey")
	}
	keyword := strings.ToLower(strings.TrimSpace(q.Get("q")))

	// 用聚合快照而不是工具缓存：界面和 AI 客户端看到的必须是同一份列表，
	// 否则"列表里有但 AI 说找不到"这类问题会反复出现。
	entries := a.svc.Registry().Current().ToolEntries()

	items := make([]hub.ToolEntry, 0, len(entries))
	for _, e := range entries {
		if serverFilter != "" && e.ServerID != serverFilter && e.ServerKey != serverFilter {
			continue
		}
		if keyword != "" && !matchesTool(e, keyword) {
			continue
		}
		items = append(items, e)
	}

	c.JSON(http.StatusOK, map[string]any{
		"total": len(items),
		"items": items,
	})
}

func matchesTool(e hub.ToolEntry, keyword string) bool {
	if strings.Contains(strings.ToLower(e.Exposed), keyword) ||
		strings.Contains(strings.ToLower(e.Description), keyword) {
		return true
	}
	return strings.Contains(strings.ToLower(e.ServerKey), keyword)
}

// --- 调用日志 ---

func (a *API) handleLogs(c *gin.Context) {
	q := c.Request.URL.Query()

	query := store.LogQuery{
		ServerID: q.Get("serverId"),
		Status:   q.Get("status"),
		Kind:     downstreamKind(q.Get("kind")),
		Limit:    queryInt(q.Get("limit"), 100),
		Offset:   queryInt(q.Get("offset"), 0),
	}

	var err error
	if query.Since, err = parseTimeParam(q.Get("since")); err != nil {
		failFor(c, err)
		return
	}
	if query.Until, err = parseTimeParam(q.Get("until")); err != nil {
		failFor(c, err)
		return
	}

	items, total, err := a.svc.Store().ListLogs(c.Request.Context(), query)
	if err != nil {
		failFor(c, err)
		return
	}
	out := make([]logItem, 0, len(items))
	for _, it := range items {
		out = append(out, toLogItem(it))
	}

	c.JSON(http.StatusOK, map[string]any{
		"total":  total,
		"limit":  query.Limit,
		"offset": query.Offset,
		"items":  out,
	})
}

func (a *API) handleClearLogs(c *gin.Context) {
	n, err := a.svc.Store().ClearLogs(c.Request.Context())
	if err != nil {
		failFor(c, err)
		return
	}
	c.JSON(http.StatusOK, map[string]any{"deleted": n})
}

// --- 接入指引 ---

type connectResponse struct {
	MCP        string            `json:"mcpEndpoint"`
	SSE        string            `json:"sseEndpoint,omitempty"`
	Health     string            `json:"healthEndpoint"`
	MCPClients map[string]string `json:"mcpServers"`
	ToolCount  int               `json:"toolCount"`
	ServerNum  int               `json:"serverCount"`
	Summary    string            `json:"summary"`
	Note       string            `json:"note"`
}

func (a *API) handleConnect(c *gin.Context) {
	snap := a.svc.Registry().Current()
	mcpURL := baseURL(c.Request) + "/mcp"

	out := connectResponse{
		MCP:        mcpURL,
		Health:     baseURL(c.Request) + "/healthz",
		MCPClients: map[string]string{"mcp-hub": mcpURL},
		ToolCount:  snap.ToolCount(),
		ServerNum:  len(snap.Servers()),
		Summary:    summaryText(snap.ToolCount(), len(snap.Servers())),
		Note: "工具名带 <serverKey>__ 前缀标识来源。若某个下游暂时不可用，" +
			"它的工具仍在列表中，但调用会返回带原因的失败结果。",
	}
	if a.opts.EnableSSE {
		out.SSE = baseURL(c.Request) + "/sse"
	}
	c.JSON(http.StatusOK, out)
}

func summaryText(tools, servers int) string {
	return "本端点是 MCP 聚合网关，已把 " + strconv.Itoa(servers) + " 个下游 server 的 " +
		strconv.Itoa(tools) + " 个工具合并到同一个端点。只配这一行 URL 即可使用全部工具。"
}

// --- 辅助 ---

func endpoints(r *http.Request, sse bool) map[string]string {
	base := baseURL(r)
	out := map[string]string{
		"mcp":    base + "/mcp",
		"health": base + "/healthz",
		"ready":  base + "/readyz",
		"admin":  base + "/api/overview",
	}
	if sse {
		out["sse"] = base + "/sse"
	}
	return out
}

// baseURL 依据请求自身推断对外地址。
//
// 用 Host 头而不是配置里的监听地址：用户可能通过反代或端口转发访问，
// 回显配置时给出的必须是"他从这个浏览器能连上的地址"。
func baseURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func queryInt(raw string, def int) int {
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	return n
}

// parseTimeParam 接受 Unix 毫秒或 RFC3339。
//
// 前者是接口内部统一的表示（界面上直接用），后者是手敲 curl 时
// 更自然的写法，两者都认能省掉一轮"格式不对"的试错。
func parseTimeParam(raw string) (time.Time, error) {
	if strings.TrimSpace(raw) == "" {
		return time.Time{}, nil
	}
	if ms, err := strconv.ParseInt(raw, 10, 64); err == nil {
		return time.UnixMilli(ms), nil
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, badRequest("时间参数 %q 无法解析：请用 Unix 毫秒或 RFC3339", raw)
	}
	return t, nil
}

// downstreamKind 把查询参数折成错误分类。
//
// 大小写不敏感：分类常量对外是大写（TIMEOUT），但没人愿意
// 为了筛个日志去按 CapsLock。
func downstreamKind(raw string) downstream.ErrorKind {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return downstream.KindNone
	}
	return downstream.ErrorKind(strings.ToUpper(raw))
}
