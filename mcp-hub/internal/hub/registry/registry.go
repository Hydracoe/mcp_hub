// Package registry 持有对外聚合视图：一份不可变的工具列表与路由表。
// 它只回答"对外有哪些工具"与"某个对外名该发给谁"，不碰连接、不碰存储；
// 快照整体替换、读路径无锁，tools/list 因此一次库都不查。
package registry

import (
	"bytes"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"sort"
	"sync/atomic"
	"time"

	"github.com/hydra/mcp-hub/internal/downstream"
	"github.com/mark3labs/mcp-go/mcp"
)

// Separator 拼接对外工具名的两段：<serverKey>__<工具名>。
const Separator = "__"

// ExposedToolName 按 <serverKey>__<toolName> 拼出对外工具名。
//
// 两段都是创建后不再变的字段，名字因此天然稳定。分类刻意不进名字：它是可变的
// 组织元数据，写进标识符就等于"改一次分类 = 对所有调用方做一次破坏性重命名"。
func ExposedToolName(serverKey, toolName string) string {
	return serverKey + Separator + toolName
}

// Route 是一条聚合工具到下游的显式映射。
//
// 路由靠查表，不靠切分 Exposed 反推——下游工具名本身可能含分隔符
// （如 a__b），字符串解析会在这种边界上串线。
type Route struct {
	ServerID  string
	ServerKey string
	ToolName  string // 下游原始名
	Exposed   string // <serverKey>__<toolName>
	Available bool
}

// HiddenTool 是"在缓存里、但不进入聚合列表"的工具。
// Enabled=false 的工具不该出现在 AI 的 tools/list 里，但管理界面必须看得到。
type HiddenTool struct {
	Info downstream.ToolInfo
}

// ToolSource 是构建快照的输入：一个 server 及其缓存的工具。
//
// 刻意不直接吃 store 类型，让聚合逻辑与持久层解耦，便于测试。
type ToolSource struct {
	ServerID  string
	ServerKey string
	Name      string
	// Category 是分类的中文展示名，只用于展示与检索，不参与工具名拼接。
	Category  string
	Enabled   bool
	Available bool // 最近一次探活是否成功
	// Tools 是"这次要暴露给 AI"的工具，即已启用且通过名单的。
	Tools []downstream.ToolInfo
	// Hidden 是同一台 server 上"不暴露"的工具，只进管理视图。
	Hidden []HiddenTool
}

// ToolEntry 是聚合列表里的一项，供管理界面展示。
// 比 mcp.Tool 多带"来自哪个下游、此刻是否可用"——AI 不需要这些，
// 但排查问题时先看这两条。
type ToolEntry struct {
	Exposed     string          `json:"exposedName"`
	Name        string          `json:"name"`
	ServerID    string          `json:"serverId"`
	ServerKey   string          `json:"serverKey"`
	Category    string          `json:"category,omitempty"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
	Available   bool            `json:"available"`
	// Enabled 是人工开关的当前值，也等价于"此刻是否进入聚合列表"：
	// server 自身的启停在 ToolSource.Enabled 上，不在条目上重复表达。
	Enabled bool `json:"enabled"`
}

// Snapshot 是一份不可变的聚合视图。工具列表变更时整份替换，
// 读路径因此无需加锁。
type Snapshot struct {
	routes  map[string]Route
	servers []ServerView
	tools   []mcp.Tool
	entries []ToolEntry
	builtAt int64
}

// ServerView 是快照中一个 server 的可见状态，供管理接口展示。
type ServerView struct {
	ServerID  string   `json:"serverId"`
	ServerKey string   `json:"serverKey"`
	Name      string   `json:"name"`
	Available bool     `json:"available"`
	ToolCount int      `json:"toolCount"`
	Tools     []string `json:"tools"`
}

// Registry 持有当前聚合快照。替换是原子的，读不加锁。
type Registry struct {
	current atomic.Pointer[Snapshot]
}

// NewRegistry 构造一个空注册表。
func NewRegistry() *Registry {
	r := &Registry{}
	r.current.Store(&Snapshot{routes: map[string]Route{}})
	return r
}

// Build 依据一组 tool source 构建快照并原子换上。
// 只接受 Enabled 的 source；不可用者仍贡献缓存工具，只标记 unavailable，避免下游抖动让
// 已聚合工具从 AI 列表里消失。tools/routes 给 AI，entries 额外含 Hidden（靠 Enabled 区分）。
func (r *Registry) Build(sources []ToolSource) *Snapshot {
	snap := &Snapshot{
		routes:  make(map[string]Route),
		builtAt: time.Now().UnixMilli(),
	}

	sorted := append([]ToolSource(nil), sources...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ServerKey < sorted[j].ServerKey })

	for _, src := range sorted {
		if !src.Enabled {
			continue
		}

		view := ServerView{
			ServerID:  src.ServerID,
			ServerKey: src.ServerKey,
			Name:      src.Name,
			Available: src.Available,
			Tools:     make([]string, 0, len(src.Tools)),
		}

		for _, t := range src.Tools {
			exposed := ExposedToolName(src.ServerKey, t.Name)

			// 兜底：serverKey 有唯一约束，重名本不该发生。
			// 宁可少暴露一个工具，也不能让调用静默打到别的下游。
			if _, dup := snap.routes[exposed]; dup {
				continue
			}

			snap.routes[exposed] = Route{
				ServerID:  src.ServerID,
				ServerKey: src.ServerKey,
				ToolName:  t.Name,
				Exposed:   exposed,
				Available: src.Available,
			}
			snap.tools = append(snap.tools, mcp.Tool{
				Name:        exposed,
				Title:       t.Name,
				Description: t.Description,
				// 原始 JSON Schema 逐字节透传：下游 schema 不可控，
				// 结构化重建只会有损。
				RawInputSchema: normalizeSchema(t.InputSchema),
			})
			snap.entries = append(snap.entries, ToolEntry{
				Exposed:     exposed,
				Name:        t.Name,
				ServerID:    src.ServerID,
				ServerKey:   src.ServerKey,
				Category:    src.Category,
				Description: t.Description,
				InputSchema: normalizeSchema(t.InputSchema),
				Available:   src.Available,
				Enabled:     true,
			})
			view.Tools = append(view.Tools, t.Name)
		}

		// 未启用的工具只进 entries，不进 routes/tools：管理界面可见，AI 列表里没有。
		for _, h := range src.Hidden {
			snap.entries = append(snap.entries, ToolEntry{
				Exposed:     ExposedToolName(src.ServerKey, h.Info.Name),
				Name:        h.Info.Name,
				ServerID:    src.ServerID,
				ServerKey:   src.ServerKey,
				Category:    src.Category,
				Description: h.Info.Description,
				InputSchema: normalizeSchema(h.Info.InputSchema),
				Available:   src.Available,
				Enabled:     false,
			})
		}

		view.ToolCount = len(view.Tools)
		snap.servers = append(snap.servers, view)
	}

	// 按对外名定序：下游返回的顺序不可控，服务端排一次，管理界面直接渲染，
	// 也让 /api/tools 的响应稳定可 diff。
	sort.Slice(snap.entries, func(i, j int) bool { return snap.entries[i].Exposed < snap.entries[j].Exposed })

	r.current.Store(snap)
	return snap
}

// Current 返回当前快照。永不为 nil。
func (r *Registry) Current() *Snapshot {
	return r.current.Load()
}

// Lookup 按对外工具名查询路由。
func (s *Snapshot) Lookup(exposed string) (Route, bool) {
	route, ok := s.routes[exposed]
	return route, ok
}

// Tools 返回聚合后的工具列表快照。
func (s *Snapshot) Tools() []mcp.Tool { return s.tools }

// ToolEntries 返回带来源与可用性标记的工具列表，供管理界面使用。
// 含未启用的工具（Enabled=false），与会下发给 AI 的列表不同。
func (s *Snapshot) ToolEntries() []ToolEntry { return s.entries }

// ToolCount 返回聚合工具总数。
func (s *Snapshot) ToolCount() int { return len(s.tools) }

// Servers 返回各 server 的可见状态。
func (s *Snapshot) Servers() []ServerView { return s.servers }

// BuiltAt 返回快照构建时刻（Unix 毫秒）。
func (s *Snapshot) BuiltAt() int64 { return s.builtAt }

// maxSchemaBytes 是单个工具 schema 的透传上限。
// schema 会原样进入每次 tools/list 响应与检索语料，超大 schema 会撑爆 AI 的上下文；
// 32KB 足以容纳任何认真设计的参数定义，超过它几乎必然是把整份文档塞进了 schema。
const maxSchemaBytes = 32 * 1024

// normalizeSchema 保证透传给 AI 客户端的 schema 是一段合法且尺寸可控的 JSON 对象。
// 只兜底不改写：畸形或非对象退化成空对象，避免整批工具装载失败；超大者降级为提示描述。
// 用首字符 '{' 而非反序列化判类型：这是 tools/list 的热路径，只需知道"是不是对象"。
func normalizeSchema(raw json.RawMessage) json.RawMessage {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' || !json.Valid(trimmed) {
		return json.RawMessage(`{"type":"object"}`)
	}
	if len(trimmed) > maxSchemaBytes {
		return json.RawMessage(fmt.Sprintf(
			`{"type":"object","description":"原始参数定义过大（%d KB，超过 %d KB 上限），已被网关省略。请依据工具描述构造合理的对象参数；需要完整定义请到管理界面查看该上游工具。"}`,
			len(trimmed)/1024, maxSchemaBytes/1024))
	}
	return trimmed
}

// Fingerprint 给一组工具定义算指纹，用于判断聚合列表是否真的变了。
// 刻意不含可用性：下游抖动不影响工具定义，不该触发列表变更通知。
func Fingerprint(tools []mcp.Tool) uint64 {
	h := fnv.New64a()
	for _, t := range tools {
		h.Write([]byte(t.Name))
		h.Write([]byte{0})
		h.Write([]byte(t.Description))
		h.Write([]byte{0})
		h.Write(t.RawInputSchema)
		h.Write([]byte{0})
	}
	return h.Sum64()
}
