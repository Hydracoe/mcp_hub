package hub

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/hydra/mcp-hub/internal/downstream"
	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

// serverInstructions 会随协商结果下发给 AI 客户端。
//
// 这段文字是 AI 使用本网关的唯一说明书：工具怎么找、错了怎么办，
// 全在这里。写成"搜（薄）→ 收窄 → 取 schema → 调用"的树式流程，
// 每步返回都很小，AI 不会因为一次模糊搜索就吞进几十个完整 schema。
const serverInstructions = `本端点是 mcp-hub 聚合网关，把多个下游 MCP server 的工具合并在同一个端点。
工具命名规则：<serverKey>__<toolName>，前缀标识来源 server。

使用方式（树式收窄，通常 3 步）：
1. 先调 hub__search_tools(query="意图") 搜索。结果只含工具名与一句话描述，
   不带参数定义；末尾 facets 是命中的分类/server 分布。
2. 看结果：命中明确（描述贴合意图、facets 集中）→ 跳到第 3 步；
   命中分散或太泛 → 带上 facets 提示的 category/server 过滤器（或换措辞）重新搜索，
   把候选收窄到 3 个以内。不确定有哪些分类或 server 时，调 hub__list_categories / hub__list_servers。
3. 对选中的 1-2 个工具调 hub__tool(name="serverKey__toolName") 获取完整参数 schema，
   然后按对外全名 tools/call 直接调用。

若你看到的工具列表只有 hub__* 开头的工具，这是正常现象：本端点运行在
"元工具模式"，按上述流程发现并调用下游工具。对候选很有把握时可在搜索时
加 with_schema=true 跳过第 3 步。

错误处理：
- 搜索不到：换更具体的措辞，或用过滤器/分类浏览收窄，不要重复同样的搜索。
- 未知工具：可能拼写有误或已被下线，用 hub__search_tools 重新搜索。
- 参数错误：错误信息里带完整参数 schema，按它修正后重试。
- 上游不可用：用 hub__list_servers 查看哪些可用，换其他 server 的同类工具。
- 限流或额度不足：稍后重试即可，不必换工具。
- 某下游暂时不可用时，它的工具仍在列表里，但调用会返回带错误说明的结果，
  请根据说明判断是稍后重试还是改用其他工具。`

// ListMode 控制 AI 客户端 tools/list 看到什么（PRD §4.7.6）。
//
// meta：只列 meta-tool，下游工具靠搜索发现——列表小，AI 规划轮次少；
// all：列出全部聚合工具。两种模式下 tools/call 的行为完全一致：
// 任何已注册工具都能直接调用。
const (
	ListModeMeta = "meta"
	ListModeAll  = "all"
)

// Deps 是聚合器依赖的外部能力，由服务编排层注入。
//
// 用函数而不是结构体字段，是为了让聚合器始终读到最新状态，
// 而不需要在配置变更时重新构造聚合器。
type Deps struct {
	// Sources 返回当前全部 server 的聚合输入。
	Sources func() []ToolSource

	// SpecOf 按 serverID 取出下游连接配置。取不到表示该 server 已删除。
	SpecOf func(serverID string) (downstream.Spec, bool)

	// Log 记录一次工具调用。可选，为 nil 时不记录。
	Log func(LogRecord)
}

// LogRecord 是一次工具调用的事实记录。
type LogRecord struct {
	ServerID  string
	ServerKey string
	ToolName  string // 下游原始名
	Exposed   string
	Duration  time.Duration
	Status    string // ok | error | key
	Kind      downstream.ErrorKind
	Err       string

	// CredentialID 归因到具体密钥，单密钥模式为空。
	// 只记 id 不记明文：日志里落一份凭据，等于把密钥抄到了另一个地方。
	CredentialID string

	// Attempt 是这次调用用掉第几次密钥尝试（从 1 开始）。
	Attempt int

	// APIKeyID 是发起调用的接入 key（外部 AI 客户端），匿名调用为空。
	// Args / Result 是请求现场（调用前已截断）：排障时"当时发了什么、
	// 拿回了什么"比任何统计都值钱。
	APIKeyID string
	Args     string
	Result   string
}

// callMeta 承载一次调用的运行信息，由 Pool 写入、Aggregator 读取。
//
// 走 ctx 而不是给 CallTool 加返回值：改了签名，所有调用点都要跟着动，
// 而"用了哪把密钥"只有日志需要，不该成为主路径上的一个必填项。
type callMeta struct {
	mu           sync.Mutex
	credentialID string
	attempts     int
}

type callMetaKey struct{}

func withCallMeta(ctx context.Context) (context.Context, *callMeta) {
	meta := &callMeta{}
	return context.WithValue(ctx, callMetaKey{}, meta), meta
}

func callMetaFrom(ctx context.Context) *callMeta {
	if ctx == nil {
		return nil
	}
	meta, _ := ctx.Value(callMetaKey{}).(*callMeta)
	return meta
}

func (m *callMeta) record(credentialID string, attempt int) {
	m.mu.Lock()
	m.credentialID = credentialID
	m.attempts = attempt
	m.mu.Unlock()
}

func (m *callMeta) snapshot() (string, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.credentialID, m.attempts
}

// Aggregator 把多个下游 server 聚合成一个对外 MCP server。
//
// 它不持有任何连接——连接在 Pool 里，工具索引在 Registry 里。
// 聚合器的职责只有两件：把工具列表拼出来，把调用路由过去。
type Aggregator struct {
	deps     Deps
	registry *Registry
	pool     *Pool
	mcp      *mcpserver.MCPServer

	metaMu sync.RWMutex
	metaFn func() []mcpserver.ServerTool

	refreshMu sync.Mutex
	lastPrint uint64
	hasPrint  bool
}

// NewAggregator 构造聚合 MCP server 并完成首次工具装载。
func NewAggregator(deps Deps, registry *Registry, pool *Pool, listMode string) *Aggregator {
	a := &Aggregator{deps: deps, registry: registry, pool: pool}
	opts := []mcpserver.ServerOption{
		// 下游上下线会改变工具列表，声明 listChanged 让客户端能跟上。
		mcpserver.WithToolCapabilities(true),
		// 聚合的是第三方 server，其 schema 与注解不可控。
		// 任何一处 panic 都不该拖垮整个端点。
		mcpserver.WithRecovery(),
		mcpserver.WithInstructions(serverInstructions),
	}
	if listMode == ListModeMeta {
		// meta 模式下 tools/list 只暴露 meta-tool。
		// 注意 call 路径也过同一条过滤器链，见 metaOnlyFilter。
		opts = append(opts, mcpserver.WithToolFilter(a.metaOnlyFilter))
	}
	a.mcp = mcpserver.NewMCPServer("mcp-hub", downstream.ClientVersion, opts...)
	a.Refresh()
	return a
}

// SetMetaTools 注册 meta-tool 提供函数。
//
// 在构造之后调用：meta-tool 的 handler 依赖 Service 自身，
// 同一次构造里拿不到。注册后触发一次刷新让它们即刻生效。
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
// mcp-go 的 list 与 call 两条路径共用同一条过滤器链：
// list 传入完整列表，call 传入只含被请求那一个工具的单元素切片。
// 因此 len==1 是 call 路径的判据——那时一律放行（已注册的工具
// 都可直接调用），否则只保留 hub__* 前缀的 meta-tool。
func (a *Aggregator) metaOnlyFilter(_ context.Context, tools []mcp.Tool) []mcp.Tool {
	if len(tools) == 1 {
		return tools
	}
	out := make([]mcp.Tool, 0, len(tools))
	for _, t := range tools {
		if strings.HasPrefix(t.Name, MetaPrefix) {
			out = append(out, t)
		}
	}
	return out
}

// MCP 返回底层 MCP server，供 HTTP 层挂载。
func (a *Aggregator) MCP() *mcpserver.MCPServer { return a.mcp }

// Registry 返回聚合路由表。
func (a *Aggregator) Registry() *Registry { return a.registry }

// Refresh 重建聚合快照并原子换上新工具列表。
//
// 串行化：两次并发刷新会让工具列表出现中间态，
// 而对 AI 客户端来说"工具列表突然抖动"比"晚 100ms 刷新"更糟。
func (a *Aggregator) Refresh() *Snapshot {
	a.refreshMu.Lock()
	defer a.refreshMu.Unlock()

	snap := a.registry.Build(a.deps.Sources())

	// 工具列表没变就不动 MCP server。SetTools 会向所有已连接客户端
	// 广播 tools/list_changed，周期探活每跑一轮就抖一次工具列表，
	// 会让 AI 不断重新规划——而这完全没必要。
	print := fingerprint(snap.Tools())
	if a.hasPrint && print == a.lastPrint {
		return snap
	}

	// meta-tool 与聚合工具同一次 SetTools 注册：SetTools 是全量替换，
	// 漏掉 meta-tool 就等于在下一次刷新后把它们从端点上抹掉。
	entries := make([]mcpserver.ServerTool, 0, len(snap.Tools())+10)
	entries = append(entries, a.metaTools()...)
	for _, t := range snap.Tools() {
		// 所有工具共用同一个 handler：它从请求里的完整工具名反查路由。
		// 每个工具各配一个闭包会让每次刷新都重建上千个函数值，
		// 而路由信息本就在 Registry 里。
		entries = append(entries, mcpserver.ServerTool{Tool: t, Handler: a.callTool})
	}
	a.mcp.SetTools(entries...)

	a.lastPrint = print
	a.hasPrint = true

	return snap
}

// callTool 是聚合端点唯一的工具处理器。
//
// 失败一律转成 isError 的结果而不是协议错误：下游故障是"这次调用
// 没成功"，不是"这个端点坏了"。返回 JSON-RPC 错误会让客户端认为
// 会话异常，而 isError 让 AI 能读到原因并自行决策。
func (a *Aggregator) callTool(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	exposed := req.Params.Name
	snap := a.registry.Current()

	route, ok := snap.Lookup(exposed)
	if !ok {
		// 正常情况下这个分支到不了：MCP 服务端会在协议层挡掉未注册的工具。
		// 它防的是刷新窗口——Registry 换上新快照与 SetTools 更新工具表之间
		// 有极短的间隔，此刻到达的请求会带着已下线工具的名字进来。
		// 返回可读的错误，而不是让调用掉进一个空的 handler 里。
		return mcp.NewToolResultErrorf(
			"未知工具 %q：它不在当前聚合列表中，可能其上游已被停用。请重新拉取工具列表。",
			exposed), nil
	}

	spec, ok := a.deps.SpecOf(route.ServerID)
	if !ok {
		return mcp.NewToolResultErrorf(
			"工具 %q 的上游 %q 已被移除。", exposed, route.ServerKey), nil
	}

	args := req.Params.RawArguments
	if len(args) == 0 {
		// 无参调用：给下游一个空对象而不是 null，
		// 否则按对象声明入参的下游会直接报参数类型错误。
		args = json.RawMessage(`{}`)
	}

	started := time.Now()
	ctx, meta := withCallMeta(ctx)
	res, err := a.pool.CallTool(ctx, spec, route.ToolName, args)
	dur := time.Since(started)
	credentialID, attempts := meta.snapshot()

	// 请求现场：入参原文 + 返回序列化。store 层统一截断。
	// 匿名调用（未启用任何接入 key）时 apiKeyID 为空串。
	caller := CallerAPIKeyFrom(ctx)

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
		Args:         string(args),
		Result:       string(resultJSON),
	})

	// 原样透传下游结果：content、isError、structuredContent 一字不改。
	// 网关只做搬运，不做语义改写——下游标了 isError 的工具失败，
	// 对 AI 来说就是这个工具失败了，不该被网关抹平成成功。
	return res, nil
}

func (a *Aggregator) logCall(rec LogRecord) {
	if a.deps.Log != nil {
		a.deps.Log(rec)
	}
}

// adviceFor 给出下一步该做什么。
//
// 这段文字是 AI 唯一能读到的"该怎么办"。给错比不给更糟：让一个被限流的
// 调用去"换个工具"，它会认定这些工具都不能用，转而放弃整条路径——
// 而事实上等几秒就恢复了。
func adviceFor(kind downstream.ErrorKind) string {
	switch kind {
	case downstream.KindNoUsableKey, downstream.KindRateLimited:
		return "这是上游的临时配额限制，稍后重试即可，不必改用其他工具。"
	case downstream.KindQuotaExhausted, downstream.KindAuthRequired:
		return "这是上游的凭据或额度问题，重试不会改善，需要在管理界面处理密钥。"
	case downstream.KindDraining:
		return "该上游正在停用收尾，待其完成后重试，或改用其他 server 的同类工具。"
	case downstream.KindUnknown:
		return "可稍后重试；若持续失败，请改用其他工具。"
	default:
		return "这是连接类问题，稍后重试即可。"
	}
}
