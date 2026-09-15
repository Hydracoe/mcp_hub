package api

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/hydra/mcp-hub/internal/downstream"
	"github.com/hydra/mcp-hub/internal/hub"
	"github.com/hydra/mcp-hub/internal/store"
	"go.uber.org/zap"
)

// probeResponse 是一次探活的诊断结果。
//
// 直接内嵌 ProbeResult：探活结果本就是给用户看的诊断信息，
// 在中间再抄一层字段，只会让它和 CLI 的输出慢慢漂移。
type probeResponse struct {
	*downstream.ProbeResult

	// Hint 是分类对应的中文排查方向。
	Hint string `json:"hint,omitempty"`
	// Exposed 是这些工具在聚合端点上的对外名（带前缀）。
	Exposed []string `json:"exposedTools,omitempty"`
}

func newProbeResponse(res *downstream.ProbeResult, serverKey string) *probeResponse {
	if res == nil {
		return nil
	}
	out := &probeResponse{ProbeResult: res}
	if !res.OK {
		out.Hint = res.Kind.Human()
	}
	for _, t := range res.Tools {
		out.Exposed = append(out.Exposed, serverKey+"__"+t.Name)
	}
	return out
}

// serverWriteResponse 是新增 / 编辑 / 启停后的响应。
type serverWriteResponse struct {
	serverDetail
	// Probe 仅在请求要求探活时出现。
	Probe *probeResponse `json:"probe,omitempty"`
	// Suggested 是新建时返回的分类候选（自动归类，PRD §4.7.3）。
	Suggested []hub.SuggestedCategory `json:"suggestedCategories,omitempty"`
}

// --- 列表与详情 ---

func (a *API) handleListServers(c *gin.Context) {
	ctx := c.Request.Context()

	servers, err := a.svc.Servers(ctx)
	if err != nil {
		failFor(c, err)
		return
	}

	// 一次读出全部工具缓存再分组，避免按 server 逐个查（典型 N+1）。
	tools, err := a.svc.Store().ListTools(ctx)
	if err != nil {
		a.log.Error("读取工具缓存失败", zap.Error(err))
	}
	cached := make(map[string]int, len(servers))
	for _, t := range tools {
		cached[t.ServerID]++
	}
	exposed := a.exposedCounts()
	draining := a.drainingMap()
	keys := a.keyStatsMap()

	items := make([]serverSummary, 0, len(servers))
	catNames := a.categoryNames(ctx)
	for _, sv := range servers {
		items = append(items, toSummary(sv, cached[sv.Spec.ID], exposed[sv.Spec.ID], draining[sv.Spec.ID], keys[sv.Spec.ID], catNames[sv.CategoryID]))
	}

	c.JSON(http.StatusOK, map[string]any{
		"total": len(items),
		"items": items,
	})
}

func (a *API) handleGetServer(c *gin.Context) {
	detail, err := a.detail(c.Request.Context(), c.Param("id"))
	if err != nil {
		failFor(c, err)
		return
	}
	c.JSON(http.StatusOK, detail)
}

// detail 组装详情视图。
func (a *API) detail(ctx context.Context, id string) (serverDetail, error) {
	sv, err := a.svc.Store().GetServer(ctx, id)
	if err != nil {
		return serverDetail{}, err
	}

	tools, err := a.svc.Store().ListToolsByServer(ctx, id)
	if err != nil {
		return serverDetail{}, err
	}
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		names = append(names, t.Name)
	}

	var drain *hub.DrainingInfo
	if info, ok := a.svc.DrainingInfo(id); ok {
		drain = &info
	}
	return toDetail(sv, len(names), a.exposedCounts()[id], names, drain, a.keyStatsOf(id), a.categoryNames(ctx)[sv.CategoryID]), nil
}

// drainingMap 取当前正在收尾的 server，键为 serverId。
func (a *API) drainingMap() map[string]*hub.DrainingInfo {
	out := make(map[string]*hub.DrainingInfo)
	for _, d := range a.svc.Pool().Draining() {
		info := d
		out[d.ServerID] = &info
	}
	return out
}

// keyStatsMap 从密钥池取各 server 的密钥概况，键为 serverId。
//
// 只读密钥池的运行态而不再查一次库：列表页对每个 server 各查一次密钥
// 是典型的 N+1，而密钥池里本来就有这份数据的实时版本。
func (a *API) keyStatsMap() map[string]keyStats {
	all := a.svc.AllKeyStatuses()
	out := make(map[string]keyStats, len(all))
	for serverID, list := range all {
		out[serverID] = summarizeKeys(list)
	}
	return out
}

// keyStatsOf 取单个 server 的密钥概况。
func (a *API) keyStatsOf(serverID string) keyStats {
	return summarizeKeys(a.svc.Pool().KeyStatusesOf(serverID))
}

func summarizeKeys(list []downstream.KeyStatus) keyStats {
	if len(list) == 0 {
		return keyStats{}
	}
	st := keyStats{Total: len(list), Pool: true}
	for _, k := range list {
		switch {
		case !k.Enabled:
			st.Stopped++
		case k.State == downstream.KeyActive:
			st.Usable++
		case k.State == downstream.KeyCooling:
			st.Cooling++
		default:
			st.Disabled++
		}
	}
	return st
}

// exposedCounts 从聚合快照取各 server 实际暴露的工具数。
//
// 快照里只有已启用的 server，因此被禁用的会缺省为 0——
// 这正是"它此刻没有对外提供任何工具"的正确表达。
func (a *API) exposedCounts() map[string]int {
	snap := a.svc.Registry().Current()
	out := make(map[string]int)
	for _, v := range snap.Servers() {
		out[v.ServerID] = v.ToolCount
	}
	return out
}

// --- 增删改 ---

func (a *API) handleCreateServer(c *gin.Context) {
	var req serverRequest
	if err := bindJSON(c, &req); err != nil {
		failFor(c, err)
		return
	}

	cfg, err := req.toConfig(nil)
	if err != nil {
		failFor(c, err)
		return
	}

	ctx := c.Request.Context()

	// 默认拒绝重复的 key：POST 的语义是"创建"，键已存在时报 409
	// 比悄悄覆盖更清楚——否则用户会以为自己新建了一个 server，
	// 而实际上某个正在服务的下游被改了配置。
	if !parseBool(c.Query("upsert"), false) {
		existing, err := a.svc.Store().GetServerByKey(ctx, cfg.Spec.Key)
		switch {
		case err == nil:
			abortErr(c, http.StatusConflict, "key_taken",
				"工具前缀 "+cfg.Spec.Key+" 已被占用；用 PUT /api/servers/"+existing.Spec.ID+
					" 更新它，或在 URL 上加 ?upsert=1 表示接受覆盖")
			return
		case !errors.Is(err, store.ErrNotFound):
			failFor(c, err)
			return
		}
	}

	applied, err := a.svc.ApplyServer(ctx, cfg)
	if err != nil {
		failFor(c, err)
		return
	}

	// 默认不探活：一次探活最长要等一整个下游超时，把它挂到创建请求上，
	// 界面就会出现"保存按钮转 30 秒"。需要一步到位时用 ?probe=1。
	probe := a.probeIfAsked(c, applied.ID, cfg.Spec.Key)

	detail, err := a.detail(ctx, applied.ID)
	if err != nil {
		failFor(c, err)
		return
	}
	c.JSON(http.StatusCreated, serverWriteResponse{serverDetail: detail, Probe: probe, Suggested: applied.SuggestedCategories})
}

func (a *API) handleUpdateServer(c *gin.Context) {
	ctx := c.Request.Context()
	id := c.Param("id")

	prev, err := a.svc.Store().GetServer(ctx, id)
	if err != nil {
		failFor(c, err)
		return
	}

	var req serverRequest
	if err := bindJSON(c, &req); err != nil {
		failFor(c, err)
		return
	}

	// 编辑前的配置作为"掩码还原"的依据，同时也保证 id 不变——
	// 换了 key 也是原地更新，不会留下第二条记录。
	cfg, err := req.toConfig(&prev)
	if err != nil {
		failFor(c, err)
		return
	}

	if _, err := a.svc.ApplyServer(ctx, cfg); err != nil {
		failFor(c, err)
		return
	}

	probe := a.probeIfAsked(c, id, cfg.Spec.Key)

	detail, err := a.detail(ctx, id)
	if err != nil {
		failFor(c, err)
		return
	}
	c.JSON(http.StatusOK, serverWriteResponse{serverDetail: detail, Probe: probe})
}

func (a *API) handleDeleteServer(c *gin.Context) {
	if err := a.svc.RemoveServer(c.Request.Context(), c.Param("id")); err != nil {
		failFor(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (a *API) handleEnableServer(enabled bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()
		id := c.Param("id")

		if err := a.svc.SetEnabled(ctx, id, enabled); err != nil {
			failFor(c, err)
			return
		}

		// 回读一次并把新状态带回去：界面点完开关要立刻更新这一行，
		// 不必为了拿状态再发一个请求。
		detail, err := a.detail(ctx, id)
		if err != nil {
			failFor(c, err)
			return
		}
		c.JSON(http.StatusOK, serverWriteResponse{serverDetail: detail})
	}
}

// --- 连通性测试 ---

func (a *API) handleTestServer(c *gin.Context) {
	ctx := c.Request.Context()
	id := c.Param("id")

	sv, err := a.svc.Store().GetServer(ctx, id)
	if err != nil {
		failFor(c, err)
		return
	}

	res, err := a.svc.RefreshTools(ctx, id)
	if err != nil {
		failFor(c, err)
		return
	}
	c.JSON(http.StatusOK, newProbeResponse(res, sv.Spec.Key))
}

func (a *API) handleTestAll(c *gin.Context) {
	report := a.svc.RefreshAll(c.Request.Context())
	c.JSON(http.StatusOK, report)
}

func (a *API) handleRefreshTools(c *gin.Context) {
	ctx := c.Request.Context()
	id := c.Param("id")

	sv, err := a.svc.Store().GetServer(ctx, id)
	if err != nil {
		failFor(c, err)
		return
	}

	res, err := a.svc.RefreshTools(ctx, id)
	if err != nil {
		failFor(c, err)
		return
	}

	// 与 test 的区别在响应：这里用户要的是"我的工具列表现在长什么样"，
	// 而不是"这一跳为什么慢"。诊断细节留给 test。
	exposed := make([]string, 0, len(res.Tools))
	for _, t := range res.Tools {
		exposed = append(exposed, sv.Spec.Key+"__"+t.Name)
	}
	c.JSON(http.StatusOK, map[string]any{
		"serverId":     id,
		"serverKey":    sv.Spec.Key,
		"ok":           res.OK,
		"toolCount":    res.ToolCount,
		"exposedTools": exposed,
		"kind":         res.Kind,
		"detail":       res.Detail,
		"hint":         res.Kind.Human(),
		"checkedAt":    res.CheckedAt.UnixMilli(),
	})
}

// probeIfAsked 在请求显式要求时顺手探活一次。
func (a *API) probeIfAsked(c *gin.Context, id, key string) *probeResponse {
	if !parseBool(c.Query("probe"), false) {
		return nil
	}
	res, err := a.svc.RefreshTools(c.Request.Context(), id)
	if err != nil {
		// 探活本身失败不该让"保存成功"变成"保存失败"：配置确实写进去了。
		a.log.Warn("保存后探活未能完成", zap.String("server", key), zap.Error(err))
		return nil
	}
	return newProbeResponse(res, key)
}

// --- 批量导入 ---

// importRequest 兼容各客户端通用的 mcpServers 配置格式。
type importRequest struct {
	MCPServers map[string]importServer `json:"mcpServers"`
}

// importServer 是导入时单个 server 的配置。
//
// 字段比下游 Spec 宽：客户端之间对同一件事有多种写法
// （type / transport、env 是对象还是数组），导入要能都认。
type importServer struct {
	URL       string            `json:"url"`
	Type      string            `json:"type"`
	Transport string            `json:"transport"`
	Headers   map[string]string `json:"headers"`

	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`

	TimeoutMS       int64    `json:"timeoutMs"`
	ProtocolVersion string   `json:"protocolVersion"`
	LegacyOnly      bool     `json:"legacyOnly"`
	ToolFilterMode  string   `json:"toolFilterMode"`
	ToolFilterList  []string `json:"toolFilterList"`
	Enabled         *bool    `json:"enabled"`
}

type importResponse struct {
	Total    int            `json:"total"`
	Imported int            `json:"imported"`
	Skipped  int            `json:"skipped"`
	Failed   int            `json:"failed"`
	Results  []importResult `json:"results"`
}

type importResult struct {
	Key         string `json:"key"`
	RenamedFrom string `json:"renamedFrom,omitempty"`
	Status      string `json:"status"` // created | updated | skipped | failed
	ServerID    string `json:"serverId,omitempty"`
	Probed      bool   `json:"probed,omitempty"`
	ToolCount   int    `json:"toolCount,omitempty"`
	Error       string `json:"error,omitempty"`
}

func (a *API) handleImportServers(c *gin.Context) {
	var req importRequest
	if err := bindJSON(c, &req); err != nil {
		failFor(c, err)
		return
	}
	if len(req.MCPServers) == 0 {
		failFor(c, badRequest("mcpServers 为空，期望形如 {\"mcpServers\": {\"key\": {…}}}"))
		return
	}

	probe := parseBool(c.Query("probe"), false)
	overwrite := parseBool(c.Query("overwrite"), false)

	keys := make([]string, 0, len(req.MCPServers))
	for k := range req.MCPServers {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	out := importResponse{Total: len(keys), Results: make([]importResult, 0, len(keys))}
	ctx := c.Request.Context()
	for _, key := range keys {
		res := a.importOne(ctx, key, req.MCPServers[key], probe, overwrite)
		switch res.Status {
		case "created", "updated":
			out.Imported++
		case "skipped":
			out.Skipped++
		default:
			out.Failed++
		}
		out.Results = append(out.Results, res)
	}

	// 导入是一个批量动作，部分失败仍返回 200：调用方要看的是
	// 每一项各自的结果，而不是一个非黑即白的整体状态。
	c.JSON(http.StatusOK, out)
}

func (a *API) importOne(ctx context.Context, rawKey string, in importServer, probe, overwrite bool) importResult {
	res := importResult{Key: rawKey}

	key := sanitizeKey(rawKey)
	if key == "" {
		res.Status = "failed"
		res.Error = "key 不含可用字符（只允许字母、数字、连字符与下划线）"
		return res
	}
	if key != rawKey {
		res.RenamedFrom = rawKey
		res.Key = key
	}

	existing, err := a.svc.Store().GetServerByKey(ctx, key)
	switch {
	case err == nil:
		if !overwrite {
			res.Status = "skipped"
			res.ServerID = existing.Spec.ID
			res.Error = "同名 server 已存在，如需覆盖请加 ?overwrite=1"
			return res
		}
	case !errors.Is(err, store.ErrNotFound):
		res.Status = "failed"
		res.Error = err.Error()
		return res
	}

	transport, err := importTransport(in)
	if err != nil {
		res.Status = "failed"
		res.Error = err.Error()
		return res
	}

	spec := downstream.Spec{
		Key:             key,
		Name:            key,
		Transport:       transport,
		URL:             strings.TrimSpace(in.URL),
		Headers:         in.Headers,
		Command:         strings.TrimSpace(in.Command),
		Args:            in.Args,
		Env:             envToSlice(in.Env),
		ProtocolVersion: strings.TrimSpace(in.ProtocolVersion),
		LegacyOnly:      in.LegacyOnly,
	}
	if in.TimeoutMS > 0 {
		spec.Timeout = time.Duration(in.TimeoutMS) * time.Millisecond
	}
	if err := spec.Validate(); err != nil {
		res.Status = "failed"
		res.Error = err.Error()
		return res
	}

	mode := store.ToolFilterMode(in.ToolFilterMode).Normalize()
	list := store.CleanToolNames(in.ToolFilterList)
	if err := mode.Validate(list); err != nil {
		res.Status = "failed"
		res.Error = err.Error()
		return res
	}

	cfg := store.Config{
		Spec:           spec,
		Enabled:        in.Enabled == nil || *in.Enabled,
		ToolFilterMode: mode,
		ToolFilterList: list,
	}
	if existing.Spec.ID != "" {
		cfg.Spec.ID = existing.Spec.ID
	}

	applied, err := a.svc.ApplyServer(ctx, cfg)
	if err != nil {
		res.Status = "failed"
		res.Error = err.Error()
		return res
	}
	id := applied.ID
	res.ServerID = id
	res.Status = "created"
	if existing.Spec.ID != "" {
		res.Status = "updated"
	}

	// 探活结果不改变导入的成败：配置确实写进去了。
	// 把探活失败表达成"已保存但连不上"，否则用户会反复重试导入，
	// 而真正要修的是那条配置。
	if probe {
		pres, err := a.svc.RefreshTools(ctx, id)
		switch {
		case err != nil:
			res.Error = "配置已保存，但探活未能完成: " + err.Error()
		default:
			res.Probed = pres.OK
			res.ToolCount = pres.ToolCount
			if !pres.OK {
				res.Error = "配置已保存，但探活失败: " + pres.Kind.Human()
			}
		}
	}
	return res
}

// importTransport 推断传输类型，兼容客户端之间的写法差异。
func importTransport(in importServer) (downstream.Transport, error) {
	raw := strings.ToLower(strings.TrimSpace(in.Transport))
	if raw == "" {
		raw = strings.ToLower(strings.TrimSpace(in.Type))
	}

	switch raw {
	case "":
		// 按配置内容推断：给了命令就是本地进程，否则是远程端点。
		if in.Command != "" {
			return downstream.TransportStdio, nil
		}
		if in.URL != "" {
			return downstream.TransportStreamableHTTP, nil
		}
		return "", errors.New("既没有 command 也没有 url，无法判断接入方式")
	case "http", "streamable-http", "streamablehttp", "streamable_http":
		return downstream.TransportStreamableHTTP, nil
	case "sse":
		return downstream.TransportSSE, nil
	case "stdio":
		return downstream.TransportStdio, nil
	}
	return "", errors.New("不支持的传输类型: " + raw)
}

func envToSlice(env map[string]string) []string {
	if len(env) == 0 {
		return nil
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+env[k])
	}
	return out
}

// sanitizeKey 把任意标识符折成合法的工具前缀。
//
// 客户端配置里的键名可能带下划线或中文，而前缀要参与工具名拼接，
// 字符集必须收紧。改名不静默进行——结果里带 renamedFrom，
// 用户能对得上号。
func sanitizeKey(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + ('a' - 'A'))
		default:
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-")
}
