package hub

import (
	"bytes"
	"encoding/json"
	"hash/fnv"
	"sort"
	"sync/atomic"
	"time"

	"github.com/hydra/mcp-hub/internal/downstream"
	"github.com/mark3labs/mcp-go/mcp"
)

// Separator 把下游 server 的 key 与其工具名拼成对外暴露的工具名。
const Separator = "__"

// Route 是一个聚合工具到下游的显式映射。
//
// 路由靠这张表查得，绝不靠切分 Exposed 反推——下游工具名本身
// 可能包含分隔符（如 a__b），字符串解析会在边界情况出错。
type Route struct {
	ServerID  string
	ServerKey string
	ToolName  string // 下游原始名
	Exposed   string // <serverKey>__<toolName>
	Available bool
}

// ToolSource 是构建快照的输入：一个 server 及其缓存的工具。
//
// 刻意不直接吃 store 类型，让聚合逻辑与持久层解耦，便于测试。
type ToolSource struct {
	ServerID  string
	ServerKey string
	Name      string
	Enabled   bool
	Available bool // 最近一次探活是否成功
	Tools     []downstream.ToolInfo
}

// ToolEntry 是聚合列表里的一项，供管理界面展示。
//
// 与 mcp.Tool 的区别：这里带上"来自哪个下游、此刻是否可用"。
// AI 客户端不需要知道这些（它只按名字调用），但排查问题时
// 这两条信息是首先要看的。
type ToolEntry struct {
	Exposed     string          `json:"exposedName"`
	Name        string          `json:"name"`
	ServerID    string          `json:"serverId"`
	ServerKey   string          `json:"serverKey"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
	Available   bool            `json:"available"`
}

// Snapshot 是一份不可变的聚合视图。
//
// 工具列表变更时整份替换，读路径因此无需加锁。
type Snapshot struct {
	routes   map[string]Route
	servers  []ServerView
	tools    []mcp.Tool
	entries  []ToolEntry
	conflict []string
	builtAt  int64
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
//
// 只接受 Enabled 的 source；不可用的 source 仍会贡献工具（沿用缓存），
// 但会被标记为 unavailable——这样已聚合的工具不会因为下游抖动而
// 从 AI 客户端的工具列表里凭空消失。
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
			exposed := src.ServerKey + Separator + t.Name

			if prev, dup := snap.routes[exposed]; dup {
				// 理论上不可能：servers.key 有唯一约束。真的撞了就记下来
				// 并保留先到者，绝不静默覆盖——覆盖会让调用悄悄打到别的下游。
				snap.conflict = append(snap.conflict,
					exposed+" 同时属于 "+prev.ServerKey+" 与 "+src.ServerKey)
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
				// 原始 JSON Schema 逐字节透传。下游的 schema 是不可控的，
				// 结构化重建只会有损；RawInputSchema 让 AI 客户端看到与
				// 下游声明完全一致的入参定义。
				RawInputSchema: normalizeSchema(t.InputSchema),
			})
			snap.entries = append(snap.entries, ToolEntry{
				Exposed:     exposed,
				Name:        t.Name,
				ServerID:    src.ServerID,
				ServerKey:   src.ServerKey,
				Description: t.Description,
				InputSchema: normalizeSchema(t.InputSchema),
				Available:   src.Available,
			})
			view.Tools = append(view.Tools, t.Name)
		}

		view.ToolCount = len(view.Tools)
		snap.servers = append(snap.servers, view)
	}

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
func (s *Snapshot) ToolEntries() []ToolEntry { return s.entries }

// ToolCount 返回聚合工具总数。
func (s *Snapshot) ToolCount() int { return len(s.tools) }

// Servers 返回各 server 的可见状态。
func (s *Snapshot) Servers() []ServerView { return s.servers }

// Conflicts 返回构建时发现的命名冲突。空表示无冲突。
func (s *Snapshot) Conflicts() []string { return s.conflict }

// BuiltAt 返回快照构建时刻（Unix 毫秒）。
func (s *Snapshot) BuiltAt() int64 { return s.builtAt }

// normalizeSchema 保证透传给 AI 客户端的 schema 是一段合法 JSON。
//
// 只做兜底，不做改写：畸形 schema 不该让整批工具装载失败，
// 退化成空对象 schema 后调用仍可进行，问题会在下游暴露。
func normalizeSchema(raw json.RawMessage) json.RawMessage {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || !json.Valid(trimmed) {
		return json.RawMessage(`{"type":"object"}`)
	}
	return trimmed
}

// fingerprint 给一组工具定义算一个指纹，用于判断聚合列表是否真的变了。
//
// 刻意不含 server 的可用性：下游抖动只影响调用结果，不影响工具定义，
// 因此不该触发工具列表变更通知。
func fingerprint(tools []mcp.Tool) uint64 {
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
