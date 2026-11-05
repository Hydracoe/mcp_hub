package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/hydra/mcp-hub/internal/downstream"
	"github.com/hydra/mcp-hub/internal/hub"
	"github.com/hydra/mcp-hub/internal/hub/registry"
	"github.com/hydra/mcp-hub/internal/store/logs"
	"github.com/hydra/mcp-hub/internal/store/servers"
	"go.uber.org/zap"
)

// statsWindow 是概览里“近期调用”的默认观察窗。
const statsWindow = 24 * time.Hour

type overviewResponse struct {
	Ready         bool  `json:"ready"`
	BuiltAt       int64 `json:"builtAt"`
	UptimeSeconds int64 `json:"uptimeSeconds"`

	Servers serverStats   `json:"servers"`
	Tools   toolStats     `json:"tools"`
	Calls   callStatsView `json:"calls"`

	// Credentials 只统计启用了密钥池的 server；为 0 表示全站走单密钥模式。
	Credentials hub.KeySummary `json:"credentials"`

	Endpoints map[string]string `json:"endpoints"`
}

type serverStats struct {
	Total    int `json:"total"`
	Enabled  int `json:"enabled"`
	Disabled int `json:"disabled"`
	Healthy  int `json:"healthy"`
	Degraded int `json:"degraded"`
	Offline  int `json:"offline"`
	Unknown  int `json:"unknown"`
	// 启用中但没有任何启用工具的 server 数。停用会级联禁用工具、重新启用
	// 不自动恢复，这种“看起来开着、AI 侧却全空”的状态必须显性提示。
	EnabledNoTools int `json:"enabledNoTools"`
}

type toolStats struct {
	// Total 是聚合快照里的工具数，即此刻真的暴露给 AI 的那一批。
	Total int `json:"total"`
	// Cached 是下游声明的工具总数（工具缓存），与 Total 不等即"有工具没暴露"。
	Cached int `json:"cached"`
	// Hidden 是缓存里有、但没进聚合列表的：人工禁用 + 被白/黑名单挡掉
	// + 所属 server 被整体停用；等于 Cached - Total，快照错位时钳到 0。
	Hidden int `json:"hidden"`
	// PendingEnable 是"探活新发现、默认禁用、从未被人工处理"的工具数。
	// 每一个都在等一个决定（启用或拒绝），是"待人工处理"告警的依据。
	PendingEnable int64 `json:"pendingEnable"`
}

// callStatsView 在原始统计上补一个失败率。
// 失败率的分母容易算错，放在一处算比散在各处更不容易出偏差。
type callStatsView struct {
	logs.CallStats
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
		if !sv.Enabled {
			// 停用项不参与健康分桶：status 是停用前最后一次探活的冻结值，
			// 数进 healthy 会混淆“主动关掉的”与“探活通过的”。
			stats.Disabled++
			continue
		}
		stats.Enabled++
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
	n, err := a.svc.Store().Servers.CountEnabledWithoutTools(ctx)
	if err != nil {
		failFor(c, err)
		return
	}
	stats.EnabledNoTools = n

	// 走 Service 的缓存读口：日志表会一直长，这个聚合扫描是概览里
	// 唯一随数据量变慢的查询。
	calls, err := a.svc.RecentStats(ctx, statsWindow)
	if err != nil {
		// 统计失败只留空调用数，概览其余信息仍然有用。
		a.log.Error("统计调用情况失败", zap.Error(err))
	}
	callView := callStatsView{CallStats: calls}
	if calls.Total > 0 {
		callView.FailureRate = float64(calls.Failed) / float64(calls.Total)
	}

	snap := a.svc.Registry().Current()
	ready, _ := a.svc.Ready()

	// 下游声明的工具总数。一次读全表再数，不按 server 逐个查。
	cachedTools, err := a.svc.Store().Servers.ListTools(ctx)
	if err != nil {
		// 与调用统计同样处理：数不出来就让“声明数”留空（=暴露数）。
		a.log.Error("统计工具缓存失败", zap.Error(err))
	}
	exposed := snap.ToolCount()
	tools := toolStats{Total: exposed, Cached: exposed, Hidden: 0}
	if len(cachedTools) > 0 {
		tools.Cached = len(cachedTools)
		// 快照与工具缓存各有真相，两次刷新之间可能错位；Hidden 是派生值，
		// 负数会让界面显示成 "-2"，钳到 0。
		tools.Hidden = max(len(cachedTools)-exposed, 0)
	}

	keys, err := a.svc.KeySummary(ctx)
	if err != nil {
		// 与调用统计同样处理：密钥统计失败不该让概览整个打不开。
		a.log.Error("统计密钥状况失败", zap.Error(err))
	}

	pending, err := a.svc.PendingEnableCount(ctx)
	if err != nil {
		// 同上：告警计数失败只归零，概览照常出。
		a.log.Error("统计待启用工具失败", zap.Error(err))
	}
	tools.PendingEnable = pending

	c.JSON(http.StatusOK, overviewResponse{
		Ready:         ready,
		BuiltAt:       snap.BuiltAt(),
		UptimeSeconds: int64(time.Since(a.opts.StartedAt).Seconds()),
		Servers:       stats,
		Tools:         tools,
		Calls:         callView,
		Credentials:   keys,
		Endpoints:     endpoints(c.Request, a.opts.EnableSSE),
	})
}

// toolListItem 是聚合快照的一条，外加一个只有管理接口才知道的标记。
// Pending 要 join tool_changes，不是快照的职责，所以在翻译层补上。
type toolListItem struct {
	registry.ToolEntry
	Pending bool `json:"pending"`
}

func (a *API) handleTools(c *gin.Context) {
	q := c.Request.URL.Query()
	serverFilter := q.Get("serverId")
	if serverFilter == "" {
		serverFilter = q.Get("serverKey")
	}
	keyword := strings.ToLower(strings.TrimSpace(q.Get("q")))

	// 用聚合快照而不是工具缓存：界面和 AI 客户端看到的必须是同一份列表。
	// 快照含未启用工具，靠 enabled 字段区分，界面才能把它们放回来。
	entries := a.svc.Registry().Current().ToolEntries()

	pending, err := a.svc.PendingEnableKeys(c.Request.Context())
	if err != nil {
		// 标记失败只影响"待启用"这一个筛选项，列表照常出。
		a.log.Error("读取待启用工具失败", zap.Error(err))
	}

	items := make([]toolListItem, 0, len(entries))
	for _, e := range entries {
		if serverFilter != "" && e.ServerID != serverFilter && e.ServerKey != serverFilter {
			continue
		}
		if keyword != "" && !matchesTool(e, keyword) {
			continue
		}
		items = append(items, toolListItem{
			ToolEntry: e,
			Pending:   pending[servers.PendingKey(e.ServerID, e.Name)],
		})
	}

	c.JSON(http.StatusOK, map[string]any{
		"total": len(items),
		"items": items,
	})
}

func matchesTool(e registry.ToolEntry, keyword string) bool {
	if strings.Contains(strings.ToLower(e.Exposed), keyword) ||
		strings.Contains(strings.ToLower(e.Description), keyword) {
		return true
	}
	return strings.Contains(strings.ToLower(e.ServerKey), keyword)
}

func (a *API) handleLogs(c *gin.Context) {
	q := c.Request.URL.Query()

	query := logs.LogQuery{
		ServerID: q.Get("serverId"),
		Status:   q.Get("status"),
		Kind:     downstreamKind(q.Get("kind")),
		// 自由文本搜索：空串 / 全空白由 store 层归一化成“不筛”，这里不重复判断。
		Q: q.Get("q"),
		// 调用方。store 层认得保留值 "anonymous"；空串表示不筛。
		APIKeyID: q.Get("apiKeyId"),
		// sort / order 不做校验：store 层归一化，非法值回退到时间倒序。
		Sort:   logs.LogSort(q.Get("sort")),
		Order:  logs.LogOrder(q.Get("order")),
		Limit:  queryInt(q.Get("limit"), 100),
		Offset: queryInt(q.Get("offset"), 0),
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

	items, total, err := a.svc.Store().Logs.List(c.Request.Context(), query)
	if err != nil {
		failFor(c, err)
		return
	}
	out := make([]logItem, 0, len(items))
	for _, it := range items {
		out = append(out, toLogItem(it))
	}
	// 补密钥展示名：界面「下游密钥」列显示标签而非一串 id。
	fillCredentialLabels(c.Request.Context(), a.svc.Store().Credentials, out)

	c.JSON(http.StatusOK, map[string]any{
		"total":  total,
		"limit":  query.Limit,
		"offset": query.Offset,
		"items":  out,
	})
}

func (a *API) handleClearLogs(c *gin.Context) {
	n, err := a.svc.Store().Logs.Clear(c.Request.Context())
	if err != nil {
		failFor(c, err)
		return
	}
	c.JSON(http.StatusOK, map[string]any{"deleted": n})
}

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
		Note: "工具名形如 <serverKey>__<toolName>，标识来源 server 与下游原始名；分类不进名字（可随时调整），" +
			"按分类找工具用 hub__list_servers(category=…) 或检索结果的 facets。" +
			"若某个下游暂时不可用，它的工具仍在列表中，但调用会返回带原因的失败结果。",
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
// 用 Host 头而不是配置里的监听地址：用户可能通过反代或端口转发访问。
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
// 前者是接口内部统一的表示，后者是手敲 curl 时更自然的写法。
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

// downstreamKind 把查询参数折成错误分类，大小写不敏感。
func downstreamKind(raw string) downstream.ErrorKind {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return downstream.KindNone
	}
	return downstream.ErrorKind(strings.ToUpper(raw))
}
