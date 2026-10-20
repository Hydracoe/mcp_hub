package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/hydra/mcp-hub/internal/downstream"
	"github.com/hydra/mcp-hub/internal/hub"
	"github.com/hydra/mcp-hub/internal/hub/pool"
	"github.com/hydra/mcp-hub/internal/hub/retrieval"
	"github.com/hydra/mcp-hub/internal/store/storeutil"
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
	Suggested []retrieval.SuggestedCategory `json:"suggestedCategories,omitempty"`
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
	tools, err := a.svc.Store().Servers.ListTools(ctx)
	if err != nil {
		a.log.Error("读取工具缓存失败", zap.Error(err))
	}
	cached := make(map[string]int, len(servers))
	enabled := make(map[string]int, len(servers))
	for _, t := range tools {
		cached[t.ServerID]++
		if t.Enabled {
			enabled[t.ServerID]++
		}
	}
	exposed := a.exposedCounts()
	draining := a.drainingMap()
	keys := a.keyStatsMap()

	items := make([]serverSummary, 0, len(servers))
	catNames := a.categoryNames(ctx)
	for _, sv := range servers {
		items = append(items, toSummary(sv, cached[sv.Spec.ID], enabled[sv.Spec.ID], exposed[sv.Spec.ID], draining[sv.Spec.ID], keys[sv.Spec.ID], catNames[sv.CategoryID]))
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
	sv, err := a.svc.Store().Servers.Get(ctx, id)
	if err != nil {
		return serverDetail{}, err
	}

	tools, err := a.svc.Store().Servers.ListToolsByServer(ctx, id)
	if err != nil {
		return serverDetail{}, err
	}
	names := make([]string, 0, len(tools))
	enabledCount := 0
	for _, t := range tools {
		names = append(names, t.Name)
		if t.Enabled {
			enabledCount++
		}
	}

	// 快照里查得到对外名的，才是此刻真正暴露的工具；可用性同样取自快照，
	// 与 AI 客户端看到的那一份保持同源。
	// 快照的 entries 现在也含未启用的工具，
	// 所以必须看 Enabled，而不是"在 entries 里出现过"。
	snap := a.svc.Registry().Current()
	exposed := make(map[string]bool)
	available := make(map[string]bool)
	for _, e := range snap.ToolEntries() {
		if e.ServerID != id {
			continue
		}
		available[e.Name] = e.Available
		if e.Enabled {
			exposed[e.Name] = true
		}
	}

	items := make([]toolItemView, 0, len(tools))
	for _, t := range tools {
		schema := json.RawMessage(t.InputSchema)
		items = append(items, toolItemView{
			Name:        t.Name,
			ExposedName: sv.Spec.Key + "__" + t.Name,
			Description: t.Description,
			InputSchema: schema,
			Enabled:     t.Enabled,
			Exposed:     exposed[t.Name],
			Available:   available[t.Name],
		})
	}

	var drain *pool.DrainingInfo
	if info, ok := a.svc.DrainingInfo(id); ok {
		drain = &info
	}
	return toDetail(sv, len(names), enabledCount, a.exposedCounts()[id], names, items, drain, a.keyStatsOf(id), a.categoryNames(ctx)[sv.CategoryID]), nil
}

// drainingMap 取当前正在收尾的 server，键为 serverId。
func (a *API) drainingMap() map[string]*pool.DrainingInfo {
	out := make(map[string]*pool.DrainingInfo)
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
	existing, err := a.svc.Store().Servers.GetByKey(ctx, cfg.Spec.Key)
	keyTaken := err == nil
	switch {
	case keyTaken:
		if !parseBool(c.Query("upsert"), false) {
			abortErr(c, http.StatusConflict, "key_taken",
				"工具前缀 "+cfg.Spec.Key+" 已被占用；用 PUT /api/servers/"+existing.Spec.ID+
					" 更新它，或在 URL 上加 ?upsert=1 表示接受覆盖")
			return
		}
		// 覆盖写已有 key 是更新：请求没带分类就沿用原分类（服务层按
		// "这条记录已有分类"处理），既不会被抹掉，也不会凭空造一个。
	case !errors.Is(err, storeutil.ErrNotFound):
		failFor(c, err)
		return
	}

	// 新建必须确定分类（PRD §4.7.3）：分类是工具名前缀段与检索组织
	// 与语义检索语料的来源，"未分类"的工具在语义组织上是缺席的。
	// 嵌入启用时允许不带分类创建——服务层会用自动归类补上（过阈值才应用），
	// 补不上会带候选列表报错，让界面引导用户选；嵌入禁用则无处可补，直接 400。
	// 显式给了分类还必须真实存在，否则 category_id 会留下悬空引用
	// ——界面上显示成一行空白分类。
	// 覆盖写已有 key 是更新，不受"新建必须给分类"这条约束。
	if !keyTaken && cfg.CategoryID == "" && a.svc.Embed() == nil {
		failFor(c, badRequest("新建 server 必须指定分类"))
		return
	}
	if cfg.CategoryID != "" {
		if _, err := a.svc.Store().Categories.Get(ctx, cfg.CategoryID); err != nil {
			failFor(c, badRequest("分类 %q 不存在", cfg.CategoryID))
			return
		}
	}

	applied, err := a.svc.ApplyServer(ctx, cfg)
	if err != nil {
		// 自动归类未达阈值：分类补不上，必须让用户从候选里选。
		// 单独成码，界面据此展示候选列表而不是干报错。
		var catReq *hub.CategoryRequiredError
		if errors.As(err, &catReq) {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
				"code":                "category_required",
				"error":               err.Error(),
				"suggestedCategories": catReq.Suggested,
			})
			return
		}
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

	prev, err := a.svc.Store().Servers.Get(ctx, id)
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

	sv, err := a.svc.Store().Servers.Get(ctx, id)
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

	sv, err := a.svc.Store().Servers.Get(ctx, id)
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

// handleProbeServer 对「尚未创建」的配置做一次干跑探活（L1–L3 + tools/list）。
//
// 新增向导要先拿到下游的工具清单，让用户勾选哪些对外暴露；此时配置
// 还没有落库，不能走「先创建再探活」——失败还得回滚。Prober 每次
// 新建连接、用完即弃，正好适合对请求体里的 Spec 直接试：不写库、
// 不动聚合快照，探活失败只是这次试的结果，不留下任何痕迹。
func (a *API) handleProbeServer(c *gin.Context) {
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

	res := downstream.NewProber(cfg.Spec).Probe(c.Request.Context())
	c.JSON(http.StatusOK, newProbeResponse(res, cfg.Spec.Key))
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
