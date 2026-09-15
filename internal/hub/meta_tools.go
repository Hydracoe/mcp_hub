package hub

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"go.uber.org/zap"
)

// MetaPrefix 是网关自带工具的命名前缀（PRD §4.7.6）。
const MetaPrefix = "hub__"

// MetaTools 是检索与发现的 meta-tool 集。
//
// handler 在调用时现读 Service 的实时数据，定义本身是静态的，
// 但必须随每次聚合 Refresh 一起注册——SetTools 是全量替换，
// 只在构造时注册一次，第一次刷新后就会被清掉。
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
			Tool: mcp.NewTool(MetaPrefix+"search_tools",
				mcp.WithDescription("按意图搜索全部下游 server 里可被调用的工具，支持关键词与语义混合检索。不确定用哪个工具时，先搜再调。结果只含工具名与一句话描述（不含参数定义）；末尾 facets 是命中的分类/server 分布，命中分散时用它收窄重搜，命中明确后用 hub__tool 取参数定义。"),
				mcp.WithString("query", mcp.Required(), mcp.Description("检索意图，如\"查询北京天气\"或\"把地址转成经纬度\"")),
				mcp.WithString("mode", mcp.Description("keyword（快，纯词面）| bm25 | hybrid（默认，语义+全文）")),
				mcp.WithString("category", mcp.Description("按分类过滤（id 或名称），收窄候选时用")),
				mcp.WithString("server", mcp.Description("按 server key 过滤，收窄候选时用")),
				mcp.WithString("tag", mcp.Description("按标签过滤")),
				mcp.WithNumber("k", mcp.Description("返回条数，默认 5，最大 50")),
				mcp.WithBoolean("with_schema", mcp.Description("结果内联完整参数 schema。仅当对候选很有把握、想省一次 hub__tool 调用时才开")),
			),
			Handler: m.searchTools,
		},
		{
			Tool: mcp.NewTool(MetaPrefix+"list_categories",
				mcp.WithDescription("列出全部工具分类及其描述，用于了解工具的组织方式。")),
			Handler: m.listCategories,
		},
		{
			Tool: mcp.NewTool(MetaPrefix+"list_servers",
				mcp.WithDescription("列出已启用的下游 server：名称、描述、标签、分类与当前可用性。"),
				mcp.WithString("category", mcp.Description("按分类过滤（id 或名称）")),
			),
			Handler: m.listServers,
		},
		{
			Tool: mcp.NewTool(MetaPrefix+"list_tools",
				mcp.WithDescription("列出聚合后的工具（分页，默认前 20 个）。按意图找工具请用 search_tools。"),
				mcp.WithString("server", mcp.Description("按 server key 过滤")),
				mcp.WithNumber("k", mcp.Description("条数，默认 20，最大 100")),
			),
			Handler: m.listTools,
		},
		{
			Tool: mcp.NewTool(MetaPrefix+"tool",
				mcp.WithDescription("按对外工具名获取完整定义，包括完整入参 schema。调用参数不确定时先查它。"),
				mcp.WithString("name", mcp.Required(), mcp.Description("对外工具名，如 weather__forecast")),
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
//
// 统一 JSON 而不是自然语言：AI 解析结构比解析散文可靠，
// 而字段名即契约，少一层自然语言歧义。
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
		Tag        string `json:"tag"`
		K          int    `json:"k"`
		WithSchema bool   `json:"with_schema"`
	}
	if err := parseArgs(req, &a); err != nil {
		return mcp.NewToolResultErrorf("参数解析失败: %v", err), nil
	}

	hits, err := m.svc.SearchTools(ctx, SearchQuery{
		Query:      a.Query,
		Mode:       a.Mode,
		Category:   a.Category,
		Server:     a.Server,
		Tag:        a.Tag,
		K:          a.K,
		WithSchema: a.WithSchema,
	})
	if err != nil {
		// 错误自愈（PRD §4.7.6）：告诉 AI 下一步可以做什么，
		// 而不是丢一个内部错误让它瞎猜。
		return mcp.NewToolResultErrorf("%v。可换更具体的措辞重试，或用 hub__list_categories / hub__list_servers 浏览。", err), nil
	}
	return jsonResult(map[string]any{
		"count":   len(hits),
		"facets":  FacetsOf(hits),
		"results": hits,
	})
}

func (m *MetaTools) listCategories(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	cats, err := m.svc.Categories(ctx)
	if err != nil {
		return mcp.NewToolResultErrorf("读取分类失败: %v", err), nil
	}
	out := make([]map[string]any, 0, len(cats))
	for _, c := range cats {
		out = append(out, map[string]any{
			"id":          c.ID,
			"name":        c.Name,
			"description": c.Description,
			"serverCount": c.ServerCount,
		})
	}
	return jsonResult(out)
}

func (m *MetaTools) listServers(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var a struct {
		Category string `json:"category"`
	}
	_ = parseArgs(req, &a)

	servers, err := m.svc.Servers(ctx)
	if err != nil {
		return mcp.NewToolResultErrorf("读取 server 列表失败: %v", err), nil
	}
	catID, _ := resolveCategory(ctx, m.svc.Store(), a.Category)

	// 可用性以聚合快照为准（含最近一次探活结果）。
	avail := make(map[string]bool)
	for _, sv := range m.svc.Registry().Current().Servers() {
		avail[sv.ServerKey] = sv.Available
	}

	out := make([]map[string]any, 0, len(servers))
	for _, sv := range servers {
		if !sv.Enabled {
			continue
		}
		if a.Category != "" && sv.CategoryID != catID {
			continue
		}
		out = append(out, map[string]any{
			"key":         sv.Spec.Key,
			"name":        sv.Spec.Name,
			"description": sv.Description,
			"tags":        sv.Tags,
			"category":    sv.CategoryID,
			// none=免鉴权，header=需要密钥或请求头。AI 据此判断
			// 调用失败时该不该引导用户去配密钥。
			"authMode":  sv.Spec.AuthModeOr(),
			"available": avail[sv.Spec.Key],
		})
	}
	return jsonResult(out)
}

func (m *MetaTools) listTools(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var a struct {
		Server string `json:"server"`
		K      int    `json:"k"`
	}
	_ = parseArgs(req, &a)
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
		if a.Server != "" && !strings.EqualFold(e.ServerKey, a.Server) {
			continue
		}
		out = append(out, map[string]any{
			"exposedName": e.Exposed,
			"name":        e.Name,
			"serverKey":   e.ServerKey,
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
		if e.Exposed == name || e.Name == name {
			return jsonResult(map[string]any{
				"exposedName": e.Exposed,
				"name":        e.Name,
				"serverKey":   e.ServerKey,
				"description": e.Description,
				"available":   e.Available,
				"inputSchema": e.InputSchema,
			})
		}
	}

	// 错误自愈（PRD §4.7.6）：未知工具时给出可执行的下一步。
	return mcp.NewToolResultErrorf(
		"工具 %q 未找到：可能拼写有误或已被下线。请用 hub__search_tools 重新搜索，或用 hub__list_tools 浏览。", name), nil
}
