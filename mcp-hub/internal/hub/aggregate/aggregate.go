// Package aggregate 把聚合后的工具表暴露成一个对外 MCP 端点：
// tools/list 出列表，tools/call 按名字路由回下游。
//
// 它既不持有连接（在 pool）也不持有工具表（在 registry），依赖由编排层
// 通过 Deps 注入，因此始终读到最新状态。
package aggregate

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hydra/mcp-hub/internal/downstream"
	"github.com/hydra/mcp-hub/internal/hub/callctx"
	"github.com/hydra/mcp-hub/internal/hub/pool"
	"github.com/hydra/mcp-hub/internal/hub/registry"
	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

// serverInstructions 会随协商结果下发给 AI 客户端，是 AI 发现与调用工具的唯一说明书：
// 按"搜（薄）→ 收窄 → 取 schema → 调用"的树式流程，每步返回都很小。
const serverInstructions = `
本端点是 mcp-hub 聚合网关，把多个下游 MCP server 的工具合并在同一个端点。
工具命名规则：<serverKey>__<toolName>，两段分别是来源 server 的 key 与下游原始工具名。
分类不进名字（它是可变的组织元数据，写进标识符就等于"改一次分类 = 对所有调用方做一次破坏性重命名"）；
要找某个分类下的工具，用 hub__list_servers(category=…) 或检索结果的 facets。

使用方式（树式收窄，通常 3 步）：
1. 先调 hub__search_tools(query="意图") 搜索。结果只含工具名与一句话描述，不带参数定义；末尾 facets 是命中的分类/server 分布。
2. 看结果：命中明确（描述贴合意图、facets 集中）→ 跳到第 3 步；命中分散或太泛 → 带上 facets 提示的 category/server 过滤器（或换措辞）重新搜索，把候选收窄到 3 个以内。不确定有哪些分类时，调 hub__list_categories；需要浏览某分类下的 server 用 hub__list_servers(category=…)，需要浏览某 server 的工具用 hub__list_tools(server=…)——两者的参数都是必填。
3. 对选中的 1-2 个工具调 hub__tool(name="<serverKey>__<toolName>") 获取完整参数 schema，然后按对外全名 tools/call 直接调用。

若你看到的工具列表只有 hub__* 开头的工具，这是正常现象：本端点运行在
"元工具模式"，按上述流程发现并调用下游工具。对候选很有把握时可在搜索时
加 with_schema=true 跳过第 3 步。

错误处理：
- 搜索不到：换更具体的措辞，或用过滤器/分类浏览收窄，不要重复同样的搜索。
- 未知工具：可能拼写有误或已被下线，用 hub__search_tools 重新搜索。
- 参数错误：错误信息里带完整参数 schema，按它修正后重试。
- 上游不可用：用 hub__list_servers(category=…) 查看该分类下哪些可用，换其他 server 的同类工具。
- 限流或额度不足：稍后重试即可，不必换工具。
- 某下游暂时不可用时，它的工具仍在列表里，但调用会返回带错误说明的结果，请根据说明判断是稍后重试还是改用其他工具。
`

// ListMode 控制 AI 客户端 tools/list 看到什么。
//
// meta：只列 meta-tool，下游工具靠搜索发现；all：列出全部聚合工具。
// 两种模式下 tools/call 行为一致，任何已注册的工具都能直接调用。
const (
	ListModeMeta = "meta"
	ListModeAll  = "all"
)

// MetaPrefix 是网关自带工具的命名前缀。
const MetaPrefix = "hub__"

// Deps 是聚合器依赖的外部能力，由服务编排层注入。
// 用函数而非结构体字段，让聚合器始终读到最新状态，无需随配置变更重建。
type Deps struct {
	// Sources 返回当前全部 server 的聚合输入。
	// ok 为 false 表示读取失败：必须保留上一份快照，不能拿空列表重建，
	// 否则一次短暂的数据库错误就会让所有下游工具消失。
	Sources func() (sources []registry.ToolSource, ok bool)

	// SpecOf 按 serverID 取出下游连接配置。取不到表示该 server 已删除。
	SpecOf func(serverID string) (downstream.Spec, bool)

	// Blocked 报告某 server 是否被连败拦截（连续探活失败）。可选，
	// 为 nil 时不拦截。拦截只挡调用，不挡探活与列表展示。
	Blocked func(serverID string) (reason string, yes bool)

	// OnCallSuccess 在真实调用成功后通知上层。可选。成功调用已证明下游可达，
	// 应解除探活连败留下的拦截，否则退避期间即使调用能通也会被继续拒绝。
	OnCallSuccess func(serverID string)

	// Log 记录一次工具调用。可选，为 nil 时不记录。
	Log func(LogRecord)
}

// Aggregator 把多个下游 server 聚合成一个对外 MCP server。
//
// 它不持有连接（连接在 Pool、工具索引在 Registry），职责只有两件：
// 拼出工具列表，把调用路由过去。
type Aggregator struct {
	deps Deps
	reg  *registry.Registry
	pl   *pool.Pool
	mcp  *mcpserver.MCPServer

	metaMu sync.RWMutex
	metaFn func() []mcpserver.ServerTool

	// metaNames 是当前已注册到端点上的元工具名集合。
	// 元工具由 SetMetaTools 显式登记，不靠工具名反推；随 Refresh 整份替换，读侧无锁。
	metaNames atomic.Pointer[metaNameSet]

	refreshMu sync.Mutex
	lastPrint uint64
	hasPrint  bool
}

// metaNameSet 是一份不可变的元工具名集合。
type metaNameSet struct {
	names map[string]struct{}
}

// NewAggregator 构造聚合 MCP server 并完成首次工具装载。
func NewAggregator(deps Deps, reg *registry.Registry, pl *pool.Pool, listMode string) *Aggregator {
	a := &Aggregator{deps: deps, reg: reg, pl: pl}
	opts := []mcpserver.ServerOption{
		// 下游上下线会改变工具列表，声明 listChanged 让客户端能跟上。
		mcpserver.WithToolCapabilities(true),
		// 聚合的是第三方 server，schema 与注解不可控，一处 panic 不该拖垮端点。
		mcpserver.WithRecovery(),
		mcpserver.WithInstructions(serverInstructions),
	}
	if listMode == ListModeMeta {
		// meta 模式下 tools/list 只暴露 meta-tool；call 路径也过同一条过滤器链，见 metaOnlyFilter。
		opts = append(opts, mcpserver.WithToolFilter(a.metaOnlyFilter))
	}
	a.mcp = mcpserver.NewMCPServer("mcp-hub", downstream.ClientVersion, opts...)
	a.Refresh()
	return a
}

// SetMetaTools 注册 meta-tool 提供函数。
// 必须在构造之后调用（handler 依赖 Service 自身），注册后触发一次刷新
// 让它们即刻生效。
func (a *Aggregator) SetMetaTools(fn func() []mcpserver.ServerTool) {
	a.metaMu.Lock()
	a.metaFn = fn
	a.metaMu.Unlock()
	a.Refresh()
}

func (a *Aggregator) metaTools() []mcpserver.ServerTool {
	a.metaMu.RLock()
	defer a.metaMu.RUnlock()
	if a.metaFn == nil {
		return nil
	}
	return a.metaFn()
}

// metaOnlyFilter 是 meta 列表模式的工具可见性过滤器。
//
// mcp-go 的 list 与 call 共用这条过滤器链：list 传入端点上已注册的全部工具，
// 只留元工具；call 只传入被请求的那一个，一律放行（否则下游工具再也调不到）。
// 判据是显式元工具名单（见 metaNames），不靠工具名反推。
func (a *Aggregator) metaOnlyFilter(_ context.Context, tools []mcp.Tool) []mcp.Tool {
	if a.containsMetaTool(tools) {
		// 列表路径：只留元工具。
		return a.keepMetaTools(tools)
	}
	if len(tools) == 1 {
		// 调用路径：被请求的那个工具，放行。
		//
		// 端点上只注册了一个工具时，列表与调用的输入无法区分：
		// 宁可多列一个工具，也不能让工具调不到。
		return tools
	}
	// 列表路径，但端点上还没有元工具（还没注册）：列表里没有任何该露的东西。
	return a.keepMetaTools(tools)
}

// containsMetaTool 报告输入里有没有元工具，是 list 与 call 两条路径的分界信号：
// 列表带全部元工具，调用最多带一个。
func (a *Aggregator) containsMetaTool(tools []mcp.Tool) bool {
	names := a.metaNameSet()
	if len(names) == 0 {
		return false
	}
	for _, t := range tools {
		if _, ok := names[t.Name]; ok {
			return true
		}
	}
	return false
}

// keepMetaTools 只保留元工具，其余（下游工具）剔除。
// 下游工具即使顶 hub__ 前缀也不留：撞名时占住名字的是后注册的真元工具（见 Refresh）。
func (a *Aggregator) keepMetaTools(tools []mcp.Tool) []mcp.Tool {
	names := a.metaNameSet()
	out := make([]mcp.Tool, 0, len(tools))
	for _, t := range tools {
		if _, ok := names[t.Name]; ok {
			out = append(out, t)
		}
	}
	return out
}

// metaNameSet 返回当前已注册的元工具名集合。永不为 nil（未注册时为空集）。
func (a *Aggregator) metaNameSet() map[string]struct{} {
	set := a.metaNames.Load()
	if set == nil {
		return nil
	}
	return set.names
}

// MCP 返回底层 MCP server，供 HTTP 层挂载。
func (a *Aggregator) MCP() *mcpserver.MCPServer { return a.mcp }

// Registry 返回聚合路由表。
func (a *Aggregator) Registry() *registry.Registry { return a.reg }

// Refresh 重建聚合快照并原子换上新工具列表。
// 用锁串行化：并发刷新会让列表出现中间态，对 AI 来说
// "列表突然抖动"比"晚 100ms 刷新"更糟。
func (a *Aggregator) Refresh() *registry.Snapshot {
	a.refreshMu.Lock()
	defer a.refreshMu.Unlock()

	snap := a.reg.Current()
	if sources, ok := a.deps.Sources(); ok {
		snap = a.reg.Build(sources)
	}

	// meta-tool 与聚合工具必须在同一次 SetTools 里注册：全量替换，
	// 漏掉就等于下次刷新时把它们从端点上抹掉。
	meta := a.metaTools()

	// 记下元工具名单，供过滤器判断。
	names := make(map[string]struct{}, len(meta))
	for _, m := range meta {
		names[m.Tool.Name] = struct{}{}
	}
	a.metaNames.Store(&metaNameSet{names: names})

	entries := make([]mcpserver.ServerTool, 0, len(snap.Tools())+len(meta))
	// 下游工具先注册、元工具后注册：同名时元工具胜出。
	// hub__* 是网关的保留命名空间，同名下游工具不得冒充成元工具。
	for _, t := range snap.Tools() {
		// 所有工具共用同一个 handler（按请求里的工具名反查路由）：
		// 每个工具各配一个闭包会让每次刷新重建上千个函数值。
		entries = append(entries, mcpserver.ServerTool{Tool: t, Handler: a.callTool})
	}
	entries = append(entries, meta...)

	// 指纹覆盖整份待注册列表，而不只是下游工具。
	//
	// 只比对下游工具会漏掉"meta-tool 刚注册"这次变化：启动时 Reload 已先刷新
	// 并把下游工具写入指纹，随后的 SetMetaTools 会因"没变"被跳过，元工具永远
	// 注册不上，meta 模式下 tools/list 会是空的。
	print := registry.Fingerprint(toolDefs(entries))
	if a.hasPrint && print == a.lastPrint {
		return snap
	}

	a.mcp.SetTools(entries...)

	a.lastPrint = print
	a.hasPrint = true

	return snap
}

// toolDefs 抽出待注册项里的工具定义，交给 Fingerprint 折算指纹。
func toolDefs(entries []mcpserver.ServerTool) []mcp.Tool {
	out := make([]mcp.Tool, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Tool)
	}
	return out
}

// callTool 是聚合端点唯一的工具处理器。
//
// 失败一律转成 isError 结果而不是协议错误：下游故障是"这次调用没成功"，
// 不是"端点坏了"；返回 JSON-RPC 错误会让客户端判定会话异常。
func (a *Aggregator) callTool(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	exposed := req.Params.Name
	snap := a.reg.Current()

	route, ok := snap.Lookup(exposed)
	if !ok {
		// 正常到不了这里，MCP 服务端会在协议层挡掉未注册工具。
		// 它防的是刷新窗口：Registry 换快照与 SetTools 更新工具表之间有
		// 极短间隔，此刻到达的请求会带着已下线工具的名字。
		return mcp.NewToolResultErrorf(
			"未知工具 %q：它不在当前聚合列表中，可能其上游已被停用。请重新拉取工具列表。",
			exposed), nil
	}

	spec, ok := a.deps.SpecOf(route.ServerID)
	if !ok {
		return mcp.NewToolResultErrorf(
			"工具 %q 的上游 %q 已被移除。", exposed, route.ServerKey), nil
	}

	// 连败拦截：上游连续多次探活失败后，调用在网关就被挡下，
	// 不再真的发往下游。列表保留该工具（恢复后无需重新发现），
	// AI 拿到的是明确的拦截说明而不是一次注定失败的真实调用。
	if a.deps.Blocked != nil {
		if reason, yes := a.deps.Blocked(route.ServerID); yes {
			return mcp.NewToolResultErrorf(
				"工具 %q 的上游 %q 已被网关暂停调用。\n原因：%s\n这是保护性措施：上游探活恢复后会自动解除，届时可重试；当前建议改用其他工具。",
				exposed, route.ServerKey, reason), nil
		}
	}

	args := req.Params.RawArguments
	if len(args) == 0 {
		// 无参调用：给下游一个空对象而不是 null，
		// 否则按对象声明入参的下游会直接报参数类型错误。
		args = json.RawMessage(`{}`)
	}

	started := time.Now()
	ctx, meta := callctx.WithMeta(ctx)
	res, err := a.pl.CallTool(ctx, spec, route.ToolName, args)
	dur := time.Since(started)
	credentialID, attempts := meta.Snapshot()

	// 请求现场：入参原文 + 返回序列化（store 层统一截断）。
	// 匿名调用时 apiKeyID 为空串；名字取鉴权时抄下的快照，不是当前值。
	caller := callctx.CallerID(ctx)
	callerName := callctx.CallerName(ctx)

	if err != nil {
		a.logCall(LogRecord{
			ServerID:     route.ServerID,
			ServerKey:    route.ServerKey,
			ToolName:     route.ToolName,
			Exposed:      exposed,
			Duration:     dur,
			Status:       "error",
			Kind:         downstream.CallErrorKind(err),
			Err:          err.Error(),
			CredentialID: credentialID,
			Attempt:      attempts,
			APIKeyID:     caller,
			APIKeyName:   callerName,
			Args:         string(args),
		})
		kind := downstream.CallErrorKind(err)
		return mcp.NewToolResultErrorf(
			"调用上游 %q 的工具 %q 失败。\n原因：%s\n详情：%v\n%s",
			route.ServerKey, route.ToolName, kind.Human(), err, adviceFor(kind),
		), nil
	}

	resultJSON, _ := json.Marshal(res)
	a.logCall(LogRecord{
		ServerID:     route.ServerID,
		ServerKey:    route.ServerKey,
		ToolName:     route.ToolName,
		Exposed:      exposed,
		Duration:     dur,
		Status:       "ok",
		CredentialID: credentialID,
		Attempt:      attempts,
		APIKeyID:     caller,
		APIKeyName:   callerName,
		Args:         string(args),
		Result:       string(resultJSON),
	})

	// 调用成功说明上游可达：解除探活连败留下的拦截。工具自身返回
	// isError 也算成功——那是业务结果，不是网络/协议失败。
	if a.deps.OnCallSuccess != nil {
		a.deps.OnCallSuccess(route.ServerID)
	}

	// 原样透传下游结果：content、isError、structuredContent 一字不改。
	// 网关只搬运不改写——下游标了 isError 就是工具失败了，不该被抹平成成功。
	return res, nil
}
