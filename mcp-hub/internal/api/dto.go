package api

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/hydra/mcp-hub/internal/downstream"
	"github.com/hydra/mcp-hub/internal/hub/pool"
	"github.com/hydra/mcp-hub/internal/store/credentials"
	"github.com/hydra/mcp-hub/internal/store/logs"
	"github.com/hydra/mcp-hub/internal/store/servers"
)

// serverSummary 是列表页一行所需的全部信息。
type serverSummary struct {
	ID        string `json:"id"`
	Key       string `json:"key"`
	Name      string `json:"name"`
	Transport string `json:"transport"`
	Target    string `json:"target"` // URL 或命令，列表直接展示

	Enabled bool   `json:"enabled"`
	Status  string `json:"status"` // healthy | degraded | offline | unknown

	// AutoEnable 打开后上游新增工具会不经人工确认直接暴露给 AI，界面需显眼展示。
	AutoEnable bool `json:"autoEnable"`

	// AuthMode 是生效的鉴权形态（AuthModeOr 推断后的值）：
	// none=免鉴权，header=需要请求头鉴权。
	AuthMode string `json:"authMode"`

	ProtocolEra     string   `json:"protocolEra,omitempty"` // modern | legacy
	ProtocolVersion string   `json:"protocolVersion,omitempty"`
	ServerName      string   `json:"serverName,omitempty"`
	ServerVersion   string   `json:"serverVersion,omitempty"`
	Capabilities    []string `json:"capabilities,omitempty"`

	// 元数据：描述供展示、标签自由、分类单值。
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	CategoryID  string   `json:"categoryId,omitempty"`
	Category    string   `json:"category,omitempty"` // 分类名，展示用

	// ToolCount 是缓存里的工具数（下游声明的全部）。
	ToolCount int `json:"toolCount"`
	// EnabledToolCount 是人工启用的工具数（未过工具级开关）。
	EnabledToolCount int `json:"enabledToolCount"`
	// ExposedToolCount 是此刻真正进入聚合池的数量（已启用且 server 已启用）。
	// server 停用或列表未刷新时可以小于 EnabledToolCount。
	ExposedToolCount int `json:"exposedToolCount"`

	// 密钥池概况。KeyPool 为 false 表示该 server 走单密钥模式，
	// 界面要把这件事说清楚，否则用户会以为“没显示密钥”等于“没配密钥”。
	KeyPool          bool `json:"keyPool"`
	KeyCount         int  `json:"keyCount"`
	UsableKeyCount   int  `json:"usableKeyCount"`
	CoolingKeyCount  int  `json:"coolingKeyCount,omitempty"`
	DisabledKeyCount int  `json:"disabledKeyCount,omitempty"`

	// Draining 表示已从聚合列表撤下但还有调用在收尾。
	// 与 Status（下游健康度）是两回事；界面应显示“停用中 · N 个调用收尾”。
	Draining         bool  `json:"draining"`
	DrainingInflight int   `json:"drainingInflight,omitempty"`
	DrainingSince    int64 `json:"drainingSince,omitempty"`

	LastCheckAt int64 `json:"lastCheckAt,omitempty"`
	// 不加 omitempty：探活失败时延迟为 0，省略掉会让界面误判成"从未探活"。
	LastLatencyMS int64  `json:"lastLatencyMs"`
	LastErrorKind string `json:"lastErrorKind,omitempty"`
	LastError     string `json:"lastError,omitempty"`
	LastErrorHint string `json:"lastErrorHint,omitempty"`
	CreatedAt     int64  `json:"createdAt"`
	UpdatedAt     int64  `json:"updatedAt"`
}

// serverConfigView 是可编辑的连接配置；密钥字段一律以 *** 回显，
// 提交时原样带回 *** 表示不改动。
type serverConfigView struct {
	URL             string            `json:"url,omitempty"`
	Headers         map[string]string `json:"headers,omitempty"`
	HeaderKeys      []string          `json:"headerKeys,omitempty"`
	AuthMode        string            `json:"authMode,omitempty"`
	Command         string            `json:"command,omitempty"`
	Args            []string          `json:"args,omitempty"`
	Env             []string          `json:"env,omitempty"`
	TimeoutMS       int64             `json:"timeoutMs"`
	ProtocolVersion string            `json:"protocolVersion,omitempty"`
	LegacyOnly      bool              `json:"legacyOnly"`
}

// toolItemView 是工具缓存里一个工具的完整视图（含人工开关状态）。
type toolItemView struct {
	Name        string          `json:"name"`
	ExposedName string          `json:"exposedName"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
	// Enabled 是工具级人工开关；禁用后不进聚合端点、不可被检索。
	Enabled bool `json:"enabled"`
	// Exposed 表示此刻是否真正进入聚合池（Enabled 且 server 已启用）；
	// Available 是下游可达性。
	Exposed   bool `json:"exposed"`
	Available bool `json:"available"`
}

// serverDetail 是详情页所需的全部信息。
type serverDetail struct {
	serverSummary
	Config serverConfigView `json:"config"`
	Tools  []string         `json:"tools,omitempty"` // 下游原始工具名
	// ToolItems 是带开关状态的完整工具清单，详情页的工具管理用它。
	ToolItems []toolItemView `json:"toolItems,omitempty"`
}

// logItem 是调用日志列表一行；用独立结构避免持久层的 Go 字段名直接
// 序列化成与全 API camelCase 契约不一致的形状。
type logItem struct {
	ID           string    `json:"id"`
	TS           time.Time `json:"ts"`
	ServerID     string    `json:"serverId"`
	ServerKey    string    `json:"serverKey"`
	ToolName     string    `json:"toolName"`
	ExposedName  string    `json:"exposedName"`
	DurationMS   int64     `json:"durationMs"`
	Status       string    `json:"status"`
	ErrorKind    string    `json:"errorKind,omitempty"`
	Error        string    `json:"error,omitempty"`
	CredentialID string    `json:"credentialId,omitempty"`

	// CredentialLabel 是该密钥当前的展示名（查询时批量回填），可随改名变化；
	// 稳定归因锚点仍是 credentialId，密钥删除时 label 为空、界面回退显示 id。
	CredentialLabel string `json:"credentialLabel,omitempty"`

	// 调用方与请求现场（落库时已截断）。APIKeyName 是调用当时的名字快照，
	// key 改名或被删除后历史行仍显示当时的名字。
	APIKeyID   string `json:"apiKeyId,omitempty"`
	APIKeyName string `json:"apiKeyName,omitempty"`
	Args       string `json:"args,omitempty"`
	Result     string `json:"result,omitempty"`
}

func toLogItem(e logs.LogEntry) logItem {
	return logItem{
		ID:           e.ID,
		TS:           e.TS,
		ServerID:     e.ServerID,
		ServerKey:    e.ServerKey,
		ToolName:     e.ToolName,
		ExposedName:  e.ExposedName,
		DurationMS:   e.DurationMS,
		Status:       e.Status,
		ErrorKind:    string(e.ErrorKind),
		Error:        e.Error,
		CredentialID: e.CredentialID,
		APIKeyID:     e.APIKeyID,
		APIKeyName:   e.APIKeyName,
		Args:         e.Args,
		Result:       e.Result,
	}
}

// fillCredentialLabels 把一页日志里引用到的密钥展示名批量补齐；
// 查询失败不让整页日志跟着挂，缺了就回退显示 id。
func fillCredentialLabels(ctx context.Context, st *credentials.Store, items []logItem) {
	ids := make([]string, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, it := range items {
		if it.CredentialID == "" {
			continue
		}
		if _, dup := seen[it.CredentialID]; dup {
			continue
		}
		seen[it.CredentialID] = struct{}{}
		ids = append(ids, it.CredentialID)
	}
	if len(ids) == 0 {
		return
	}

	creds, err := st.GetByIDs(ctx, ids)
	if err != nil {
		return
	}
	labels := make(map[string]string, len(creds))
	for _, c := range creds {
		labels[c.ID] = c.LabelOrFallback()
	}
	for i := range items {
		if label, ok := labels[items[i].CredentialID]; ok {
			items[i].CredentialLabel = label
		}
	}
}

// serverRequest 是新增 / 编辑 server 的请求体，语义为整体替换。
// 例外是 headers / env：必须区分“没带”与“显式清空”，否则会悄悄抹掉凭据。
type serverRequest struct {
	Key       string `json:"key"`
	Name      string `json:"name"`
	Transport string `json:"transport"`

	URL string `json:"url"`

	// Headers 三态：没带（nil）沿用原值；显式 {} 清空；非空整体替换，
	// 值为 *** 的按键还原（见 mergeSecrets）。JSON null 与“没带”同义。
	// CategoryID 使用同一套三态语义。
	Headers map[string]string `json:"headers"`

	// AuthMode：none=无需鉴权，header=需要请求头鉴权，空=按 headers 推断。
	AuthMode string `json:"authMode"`

	Command string   `json:"command"`
	Args    []string `json:"args"`

	// Env 是 stdio 的环境变量（KEY=VALUE），三态与 Headers 相同：
	// 没带 = 沿用，显式空数组 = 清空，非空 = 整体替换（*** 按原值还原）。
	Env []string `json:"env"`

	TimeoutMS       int64  `json:"timeoutMs"`
	ProtocolVersion string `json:"protocolVersion"`
	LegacyOnly      bool   `json:"legacyOnly"`

	// AutoEnable 是该 server 的“上游新增工具直接放行”开关；为空表示不指定：
	// 新增默认关闭，编辑保留原值。打开前请确认上游可信。
	AutoEnable *bool `json:"autoEnable"`

	// EnabledTools 是创建时的初始启用名单，只在首次创建时生效；
	// 编辑请求带它会被忽略，之后的启停走 /tools/:name/enable|disable。
	EnabledTools []string `json:"enabledTools,omitempty"`

	// 元数据。
	Description string   `json:"description"`
	Tags        []string `json:"tags"`

	// CategoryID 用指针区分“没带这个字段”与“显式空串”：前者沿用原值，
	// 后者退回未分类；否则一次没带该字段的保存会抹掉原分类。
	CategoryID *string `json:"categoryId"`

	// Enabled 为空表示"不指定"：新增时默认启用，编辑时保留原状态。
	Enabled *bool `json:"enabled"`
}

// toConfig 把请求折算成可落库的配置。
// prev 用于把掩码换回真实值，并在请求体没带 headers / env 时沿用原值。
func (req serverRequest) toConfig(prev *servers.Server) (servers.Config, error) {
	spec := downstream.Spec{
		Key:             strings.TrimSpace(req.Key),
		Name:            strings.TrimSpace(req.Name),
		Transport:       downstream.Transport(req.Transport),
		URL:             strings.TrimSpace(req.URL),
		Headers:         req.Headers,
		AuthMode:        strings.TrimSpace(req.AuthMode),
		Command:         strings.TrimSpace(req.Command),
		Args:            req.Args,
		Env:             req.Env,
		LegacyOnly:      req.LegacyOnly,
		ProtocolVersion: strings.TrimSpace(req.ProtocolVersion),
	}
	if req.TimeoutMS > 0 {
		spec.Timeout = time.Duration(req.TimeoutMS) * time.Millisecond
	}
	if spec.Name == "" {
		spec.Name = spec.Key
	}
	if prev != nil {
		spec.ID = prev.Spec.ID
		// headers / env 的三态在这里落地（见 mergeSecrets）。
		spec.Headers = mergeSecrets(req.Headers, prev.Spec.Headers)
		spec.Env = mergeEnvSecrets(req.Env, prev.Spec.Env)
	}

	if err := spec.Validate(); err != nil {
		return servers.Config{}, badRequest("配置不合法: %v", err)
	}

	enabled := true
	if prev != nil {
		enabled = prev.Enabled
	}
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	// 引导名单只在创建时有意义：编辑请求即使带了这个字段也不落库。
	var enabledTools []string
	if prev == nil {
		enabledTools = servers.CleanToolNames(req.EnabledTools)
	}

	autoEnable := false
	if prev != nil {
		autoEnable = prev.AutoEnable
	}
	if req.AutoEnable != nil {
		autoEnable = *req.AutoEnable
	}

	// 分类：没带就沿用原值，显式空串才清空分类。
	categoryID := ""
	clearCategory := false
	if req.CategoryID != nil {
		categoryID = strings.TrimSpace(*req.CategoryID)
		clearCategory = categoryID == ""
	}

	return servers.Config{
		Spec:          spec,
		Enabled:       enabled,
		AutoEnable:    autoEnable,
		EnabledTools:  enabledTools,
		Description:   strings.TrimSpace(req.Description),
		Tags:          req.Tags,
		CategoryID:    categoryID,
		ClearCategory: clearCategory,
	}, nil
}

// keyStats 是一个 server 的密钥池概况。
type keyStats struct {
	Total    int
	Usable   int
	Cooling  int
	Disabled int
	Stopped  int // 人工停用的
	Pool     bool
}

// toSummary 把一个存储行折算成列表视图。
//
// drain 非空表示这个 server 已撤下但仍有调用在收尾。
func toSummary(sv servers.Server, cachedTools, enabledTools, exposedTools int, drain *pool.DrainingInfo, keys keyStats, catName string) serverSummary {
	out := serverSummary{
		ID:               sv.Spec.ID,
		Key:              sv.Spec.Key,
		Name:             sv.Spec.Name,
		Transport:        string(sv.Spec.Transport),
		Target:           targetOf(sv.Spec),
		Enabled:          sv.Enabled,
		AutoEnable:       sv.AutoEnable,
		Status:           sv.Status,
		AuthMode:         sv.Spec.AuthModeOr(),
		ProtocolEra:      sv.ProtocolEra,
		ProtocolVersion:  sv.Spec.ProtocolVersion,
		ServerName:       sv.ServerName,
		ServerVersion:    sv.ServerVersion,
		Capabilities:     sv.Capabilities,
		Description:      sv.Description,
		Tags:             sv.Tags,
		CategoryID:       sv.CategoryID,
		Category:         catName,
		ToolCount:        cachedTools,
		EnabledToolCount: enabledTools,
		ExposedToolCount: exposedTools,
		LastLatencyMS:    sv.LastLatencyMS,
		LastErrorKind:    string(sv.LastErrorKind),
		LastError:        sv.LastError,
		CreatedAt:        sv.CreatedAt.UnixMilli(),
		UpdatedAt:        sv.UpdatedAt.UnixMilli(),

		KeyPool:          keys.Pool,
		KeyCount:         keys.Total,
		UsableKeyCount:   keys.Usable,
		CoolingKeyCount:  keys.Cooling,
		DisabledKeyCount: keys.Disabled + keys.Stopped,
	}
	applyDraining(&out, drain)
	if sv.Status == "unknown" && sv.LastErrorKind == downstream.KindNone {
		// 从未探活过。区分“没测过”与“测了失败”，两者下一步动作不同。
		out.LastErrorHint = "尚未探活"
	} else if sv.LastErrorKind != downstream.KindNone {
		out.LastErrorHint = sv.LastErrorKind.Human()
	}
	if !sv.LastCheckAt.IsZero() {
		out.LastCheckAt = sv.LastCheckAt.UnixMilli()
	}
	return out
}

// applyDraining 把停用收尾进度填进摘要视图。
func applyDraining(out *serverSummary, drain *pool.DrainingInfo) {
	if drain == nil {
		return
	}
	out.Draining = true
	out.DrainingInflight = drain.Inflight
	out.DrainingSince = drain.StartedAt.UnixMilli()
}

// toDetail 在摘要之上补齐可编辑配置。
func toDetail(sv servers.Server, cachedTools, enabledTools, exposedTools int, toolNames []string, toolItems []toolItemView, drain *pool.DrainingInfo, keys keyStats, catName string) serverDetail {
	cfg := serverConfigView{
		URL:             sv.Spec.URL,
		Command:         sv.Spec.Command,
		Args:            sv.Spec.Args,
		AuthMode:        sv.Spec.AuthModeOr(),
		TimeoutMS:       sv.Spec.TimeoutOr().Milliseconds(),
		ProtocolVersion: sv.Spec.ProtocolVersion,
		LegacyOnly:      sv.Spec.LegacyOnly,
	}
	if len(sv.Spec.Headers) > 0 {
		cfg.Headers = maskMap(sv.Spec.Headers)
		cfg.HeaderKeys = sortedKeys(sv.Spec.Headers)
	}
	if len(sv.Spec.Env) > 0 {
		cfg.Env = maskEnv(sv.Spec.Env)
	}

	return serverDetail{
		serverSummary: toSummary(sv, cachedTools, enabledTools, exposedTools, drain, keys, catName),
		Config:        cfg,
		Tools:         toolNames,
		ToolItems:     toolItems,
	}
}

// targetOf 给出列表里那一列可读的"指向"。
func targetOf(spec downstream.Spec) string {
	if spec.IsRemote() {
		return spec.URL
	}
	if len(spec.Args) == 0 {
		return spec.Command
	}
	return spec.Command + " " + strings.Join(spec.Args, " ")
}

// maskMap 把全部请求头替换成掩码，只保留键名；不区分“哪些头算机密”，
// 判断规则一旦写死就会漏。
func maskMap(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for k := range m {
		out[k] = secretMask
	}
	return out
}

// maskEnv 只掩码 KEY=VALUE 里的值，键名保留。
func maskEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		name, value, found := strings.Cut(kv, "=")
		if !found || value == "" {
			out = append(out, kv)
			continue
		}
		out = append(out, name+"="+secretMask)
	}
	return out
}

// mergeSecrets 把回显掩码换回真实值，并落实三态：
// next == nil 沿用 prev；len(next)==0 清空；否则整体替换、*** 从 prev 还原。
// 界面会把 *** 原样提交，直接落库等于每次编辑都抹掉凭据。
func mergeSecrets(next, prev map[string]string) map[string]string {
	if next == nil {
		// 没带这个字段：沿用原值；返回副本，避免与库里那份共享内存。
		return maps.Clone(prev)
	}
	if len(next) == 0 {
		// 显式给了空 map：这就是"清空这些密钥"的明写意图。
		return map[string]string{}
	}
	out := make(map[string]string, len(next))
	for k, v := range next {
		if v == secretMask {
			if old, ok := prev[k]; ok {
				out[k] = old
				continue
			}
			// 原配置里没有这个键时，掩码就是字面量 ***，照原样存下去。
		}
		out[k] = v
	}
	return out
}

// mergeEnvSecrets 同 mergeSecrets，作用于 KEY=VALUE 列表。
func mergeEnvSecrets(next, prev []string) []string {
	if next == nil {
		return slices.Clone(prev)
	}
	if len(next) == 0 {
		return []string{}
	}
	prevMap := make(map[string]string, len(prev))
	for _, kv := range prev {
		if name, value, ok := strings.Cut(kv, "="); ok {
			prevMap[name] = value
		}
	}

	out := make([]string, 0, len(next))
	for _, kv := range next {
		name, value, found := strings.Cut(kv, "=")
		if found && value == secretMask {
			if old, ok := prevMap[name]; ok {
				out = append(out, name+"="+old)
				continue
			}
		}
		out = append(out, kv)
	}
	return out
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
