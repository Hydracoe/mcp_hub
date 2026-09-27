package hub

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/hydra/mcp-hub/internal/hub/aggregate"
	"github.com/hydra/mcp-hub/internal/hub/retrieval"
	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"go.uber.org/zap"
)

// MetaTools 是检索与发现的 meta-tool 集。
//
// handler 调用时现读 Service 的实时数据；定义本身虽静态，却必须随每次
// 聚合 Refresh 一起注册——SetTools 是全量替换，只注册一次会被清掉。
type MetaTools struct {
	svc *Service
	log *zap.Logger
}

// NewMetaTools 构造 meta-tool 集。
func NewMetaTools(svc *Service, log *zap.Logger) *MetaTools {
	if log == nil {
		log = zap.NewNop()
	}
	return &MetaTools{svc: svc, log: log}
}

// Tools 返回全部 meta-tool 的定义与 handler。
func (m *MetaTools) Tools() []mcpserver.ServerTool {
	return []mcpserver.ServerTool{
		{
			Tool: mcp.NewTool(aggregate.MetaPrefix+"search_tools",
				mcp.WithDescription("按意图搜索全部下游 server 里可被调用的工具，支持关键词与语义混合检索。不确定用哪个工具时，先搜再调。结果只含工具名与一句话描述（不含参数定义）；末尾 facets 是命中的分类/server 分布，命中分散时用它收窄重搜，命中明确后用 hub__tool 取参数定义。"),
				mcp.WithString("query", mcp.Required(), mcp.Description("检索意图，如\"查询北京天气\"或\"把地址转成经纬度\"")),
				mcp.WithString("mode", mcp.Description("bm25（全文）| hybrid（默认，语义+全文）")),
				mcp.WithString("category", mcp.Description("按分类过滤（id 或名称），收窄候选时用")),
				mcp.WithString("server", mcp.Description("按 server key 过滤，收窄候选时用")),
				mcp.WithBoolean("with_schema", mcp.Description("结果内联完整参数 schema。仅当对候选很有把握、想省一次 hub__tool 调用时才开")),
			),
			Handler: m.searchTools,
		},
		{
			Tool: mcp.NewTool(aggregate.MetaPrefix+"list_categories",
				mcp.WithDescription("列出全部工具分类及其描述，用于了解工具的组织方式。serverCount/toolCount 只数 AI 实际可调用的：停用的 server、没有任何可调用工具的 server、未启用的工具都不计入。")),
			Handler: m.listCategories,
		},
		{
			Tool: mcp.NewTool(aggregate.MetaPrefix+"list_servers",
				mcp.WithDescription("按分类列出已启用的下游 server：名称、描述、标签、分类与当前可用性。没有任何可调用工具的 server 不会列出。不确定有哪些分类时，先调 hub__list_categories。"),
				mcp.WithString("category", mcp.Required(), mcp.Description("分类的 id 或名称，必填")),
			),
			Handler: m.listServers,
		},
		{
			Tool: mcp.NewTool(aggregate.MetaPrefix+"list_tools",
				mcp.WithDescription("列出某个 server 聚合后的工具（分页，默认前 20 个）。按意图找工具请用 search_tools；不确定有哪些 server key 时，先调 hub__list_servers。"),
				mcp.WithString("server", mcp.Required(), mcp.Description("server key，必填")),
				mcp.WithNumber("k", mcp.Description("条数，默认 20，最大 100")),
			),
			Handler: m.listTools,
		},
		{
			Tool: mcp.NewTool(aggregate.MetaPrefix+"tool",
				mcp.WithDescription("按对外工具名获取完整定义，包括完整入参 schema。调用参数不确定时先查它。"),
				mcp.WithString("name", mcp.Required(), mcp.Description("对外工具名，如 weather__openweather__forecast")),
			),
			Handler: m.tool,
		},
	}
}

// parseArgs 把工具入参反序列化到 v；无参调用（空对象）是合法的。
func parseArgs(req mcp.CallToolRequest, v any) error {
	raw := req.Params.RawArguments
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, v)
}

// jsonResult 把结果序列化成 JSON 文本返回。
// 统一 JSON 而非自然语言：字段名即契约，AI 解析结构比解析散文可靠。
func jsonResult(v any) (*mcp.CallToolResult, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return mcp.NewToolResultText(string(b)), nil
}

func (m *MetaTools) searchTools(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var a struct {
		Query      string `json:"query"`
		Mode       string `json:"mode"`
		Category   string `json:"category"`
		Server     string `json:"server"`
		WithSchema bool   `json:"with_schema"`
	}
	if err := parseArgs(req, &a); err != nil {
		return mcp.NewToolResultErrorf("参数解析失败: %v", err), nil
	}

	hits, err := m.svc.SearchTools(ctx, retrieval.SearchQuery{
		Query:      a.Query,
		Mode:       a.Mode,
		Category:   a.Category,
		Server:     a.Server,
		WithSchema: a.WithSchema,
	})
	if err != nil {
		// 错误自愈（PRD §4.7.6）：告诉 AI 下一步可以做什么，
		// 而不是丢一个内部错误让它瞎猜。
		return mcp.NewToolResultErrorf("%v。可换更具体的措辞重试，或用 hub__list_categories / hub__list_servers 浏览。", err), nil
	}
	return jsonResult(map[string]any{
		"count":   len(hits),
		"facets":  retrieval.FacetsOf(hits),
		"results": hits,
	})
}

func (m *MetaTools) listCategories(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	cats, err := m.svc.Categories(ctx)
	if err != nil {
		return mcp.NewToolResultErrorf("读取分类失败: %v", err), nil
	}

	// 计数是给 AI 的导航信号，必须与 hub__list_servers / 检索实际能给出的
	// 一致（T22 的教训：说明书与实现不符，AI 会照着空跑）。库里的原始计数
	// 含停用 server 与未启用工具，那是管理口径；这里换算成"AI 可用"口径：
	// 只数已启用且至少有一个已启用工具的 server，工具数只数已启用的。
	servers, err := m.svc.Servers(ctx)
	if err != nil {
		return mcp.NewToolResultErrorf("读取 server 列表失败: %v", err), nil
	}
	exposed := make(map[string]int) // serverKey → 对外可调工具数
	for _, sv := range m.svc.Registry().Current().Servers() {
		exposed[sv.ServerKey] = sv.ToolCount
	}
	type tally struct{ servers, tools int }
	byCat := make(map[string]tally, len(cats))
	for _, sv := range servers {
		if !sv.Enabled {
			continue
		}
		n := exposed[sv.Spec.Key]
		if n == 0 {
			continue // 与 list_servers 同规则：没有可调用工具的 server 不算数
		}
		t := byCat[sv.CategoryID]
		t.servers++
		t.tools += n
		byCat[sv.CategoryID] = t
	}

	out := make([]map[string]any, 0, len(cats))
	for _, c := range cats {
		t := byCat[c.ID]
		out = append(out, map[string]any{
			"id":   c.ID,
			"name": c.Name,
			// slug 供 AI 按分类筛工具名，中文名给人看，两个都给。
			"slug":        c.Slug,
			"description": c.Description,
			"serverCount": t.servers,
			"toolCount":   t.tools,
		})
	}
	return jsonResult(out)
}

func (m *MetaTools) listServers(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var a struct {
		Category string `json:"category"`
	}
	if err := parseArgs(req, &a); err != nil {
		return mcp.NewToolResultErrorf("参数解析失败: %v", err), nil
	}
	a.Category = strings.TrimSpace(a.Category)
	// category 必填（错误自愈）：全量 server 列表对 AI 只是一堆待筛候选，
	// 报错并指路比返回长列表更省轮次。
	if a.Category == "" {
		return mcp.NewToolResultErrorf("category 必填：先用 hub__list_categories 查看有哪些分类，再按其 id 或名称过滤。"), nil
	}

	servers, err := m.svc.Servers(ctx)
	if err != nil {
		return mcp.NewToolResultErrorf("读取 server 列表失败: %v", err), nil
	}
	catID, err := retrieval.ResolveCategory(ctx, m.svc.Store(), a.Category)
	if err != nil {
		return mcp.NewToolResultErrorf("解析分类 %q 失败: %v", a.Category, err), nil
	}

	// 可用性与可调工具数都以聚合快照为准（含最近一次探活结果与人工开关）。
	type view struct {
		available bool
		tools     int
	}
	snap := make(map[string]view)
	for _, sv := range m.svc.Registry().Current().Servers() {
		snap[sv.ServerKey] = view{available: sv.Available, tools: sv.ToolCount}
	}

	out := make([]map[string]any, 0, len(servers))
	for _, sv := range servers {
		if !sv.Enabled {
			continue
		}
		if sv.CategoryID != catID {
			continue
		}
		v, ok := snap[sv.Spec.Key]
		// 没有任何可调用工具的 server 对 AI 不存在：列出一台"什么都调不了"
		// 的 server，AI 只会白跑一轮 list_tools 再困惑。工具的启停决定的是
		// 发现面，不是 server 的生死——管理接口照常能看到这台 server。
		if !ok || v.tools == 0 {
			continue
		}
		out = append(out, map[string]any{
			"key":         sv.Spec.Key,
			"name":        sv.Spec.Name,
			"description": sv.Description,
			"tags":        sv.Tags,
			"category":    sv.CategoryID,
			// none=免鉴权，header=需要密钥或请求头。AI 据此判断调用失败时
			// 该不该引导用户去配密钥。
			"authMode":  sv.Spec.AuthModeOr(),
			"available": v.available,
		})
	}
	return jsonResult(out)
}

func (m *MetaTools) listTools(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var a struct {
		Server string `json:"server"`
		K      int    `json:"k"`
	}
	if err := parseArgs(req, &a); err != nil {
		return mcp.NewToolResultErrorf("参数解析失败: %v", err), nil
	}
	// server 必填（错误自愈）：不带 server 的全量工具列表会吞进分页也装
	// 不下的条目，报错并指路比返回前 20 个更省轮次。
	a.Server = strings.TrimSpace(a.Server)
	if a.Server == "" {
		return mcp.NewToolResultErrorf("server 必填：不确定有哪些 server key 时，先用 hub__list_categories / hub__list_servers 查看再过滤。"), nil
	}
	k := a.K
	if k <= 0 {
		k = 20
	}
	if k > 100 {
		k = 100
	}

	entries := m.svc.Registry().Current().ToolEntries()
	out := make([]map[string]any, 0, k)
	for _, e := range entries {
		if !strings.EqualFold(e.ServerKey, a.Server) {
			continue
		}
		// 未启用的工具只属于管理界面。这是给 AI 的浏览入口：列出来的每一个
		// 都必须是真能调的——推一个调不动的工具，AI 只会白试一轮还很困惑。
		// entries 里带 Enabled=false 的那些，与 all 模式下 tools/list 看不到
		// 的是同一批，两条入口必须一致。
		if !e.Enabled {
			continue
		}
		out = append(out, map[string]any{
			"exposedName": e.Exposed,
			"name":        e.Name,
			"serverKey":   e.ServerKey,
			"category":    e.Category,
			"description": e.Description,
			"available":   e.Available,
		})
		if len(out) >= k {
			break
		}
	}
	return jsonResult(out)
}

func (m *MetaTools) tool(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var a struct {
		Name string `json:"name"`
	}
	if err := parseArgs(req, &a); err != nil {
		return mcp.NewToolResultErrorf("参数解析失败: %v", err), nil
	}
	name := strings.TrimSpace(a.Name)
	if name == "" {
		return mcp.NewToolResultErrorf("name 必填"), nil
	}

	for _, e := range m.svc.Registry().Current().ToolEntries() {
		if e.Exposed != name && e.Name != name {
			continue
		}
		// 未启用的工具不该给出定义：给一份 schema 却调不动，比直接说没找到
		// 更糟——AI 会拿着参数去试，失败后还不知道是"没启用"还是"参数错"。
		if !e.Enabled {
			break
		}
		return jsonResult(map[string]any{
			"exposedName": e.Exposed,
			"name":        e.Name,
			"serverKey":   e.ServerKey,
			"category":    e.Category,
			"description": e.Description,
			"available":   e.Available,
			"inputSchema": e.InputSchema,
		})
	}

	// 错误自愈（PRD §4.7.6）：未知工具时给出可执行的下一步。
	return mcp.NewToolResultErrorf(
		"工具 %q 未找到：可能拼写有误或已被下线。请用 hub__search_tools 重新搜索，或用 hub__list_tools 浏览。", name), nil
}
