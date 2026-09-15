package api

import (
	"sort"
	"strings"
	"time"

	"github.com/hydra/mcp-hub/internal/downstream"
	"github.com/hydra/mcp-hub/internal/hub"
	"github.com/hydra/mcp-hub/internal/store"
)

// --- 输出结构 ---

// serverSummary 是列表页一行所需的全部信息。
type serverSummary struct {
	ID        string `json:"id"`
	Key       string `json:"key"`
	Name      string `json:"name"`
	Transport string `json:"transport"`
	Target    string `json:"target"` // URL 或命令，列表直接展示

	Enabled bool   `json:"enabled"`
	Status  string `json:"status"` // healthy | degraded | offline | unknown

	// AuthMode 是生效的鉴权形态（AuthModeOr 推断后的值）：
	// none=免鉴权，header=需要请求头鉴权。界面据此区分
	// "这个下游天生不需要密钥"和"要密钥但还没配"。
	AuthMode string `json:"authMode"`

	ProtocolEra     string   `json:"protocolEra,omitempty"` // modern | legacy
	ProtocolVersion string   `json:"protocolVersion,omitempty"`
	ServerName      string   `json:"serverName,omitempty"`
	ServerVersion   string   `json:"serverVersion,omitempty"`
	Capabilities    []string `json:"capabilities,omitempty"`

	// 元数据（PRD §4.7）：描述供展示、标签自由、分类单值。
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	CategoryID  string   `json:"categoryId,omitempty"`
	Category    string   `json:"category,omitempty"` // 分类名，展示用

	// ToolCount 是缓存里的工具数（下游声明的全部）。
	ToolCount int `json:"toolCount"`
	// ExposedToolCount 是过滤后真正进入聚合池的数量。
	// 与 ToolCount 不等，就说明过滤规则正在生效。
	ExposedToolCount int `json:"exposedToolCount"`

	ToolFilterMode string   `json:"toolFilterMode"`
	ToolFilterList []string `json:"toolFilterList,omitempty"`

	// 密钥池概况。KeyPool 为 false 表示该 server 走单密钥模式
	// （沿用 config.headers），界面上要把这件事说清楚，
	// 否则用户会以为"没显示密钥"等于"没配密钥"。
	KeyPool          bool `json:"keyPool"`
	KeyCount         int  `json:"keyCount"`
	UsableKeyCount   int  `json:"usableKeyCount"`
	CoolingKeyCount  int  `json:"coolingKeyCount,omitempty"`
	DisabledKeyCount int  `json:"disabledKeyCount,omitempty"`

	// EstTokensPerCall 是 TPM 的预估回退值，0 表示不计量。
	EstTokensPerCall int `json:"estTokensPerCall,omitempty"`

	// Draining 表示这个 server 已经从聚合列表撤下，但还有调用在收尾。
	//
	// 单独给这三个字段而不是塞进 Status，是因为它们是两回事：
	// Status 说的是"下游健不健康"，Draining 说的是"网关打算怎么处置它"。
	// 界面据此显示"停用中 · 2 个调用收尾"，而不是直接跳到"已停用"——
	// 后者会让用户以为当场就断干净了，与事实不符。
	Draining         bool  `json:"draining"`
	DrainingInflight int   `json:"drainingInflight,omitempty"`
	DrainingSince    int64 `json:"drainingSince,omitempty"`

	LastCheckAt   int64  `json:"lastCheckAt,omitempty"`
	LastLatencyMS int64  `json:"lastLatencyMs,omitempty"`
	LastErrorKind string `json:"lastErrorKind,omitempty"`
	LastError     string `json:"lastError,omitempty"`
	LastErrorHint string `json:"lastErrorHint,omitempty"`
	CreatedAt     int64  `json:"createdAt"`
	UpdatedAt     int64  `json:"updatedAt"`
}

// serverConfigView 是可编辑的连接配置。
//
// 密钥字段一律以 *** 回显；提交时原样带回 *** 表示"不改动"。
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

	// EstTokensPerCall 是 TPM 的预估回退值。0 表示不计量——
	// 界面必须显示"未计量"而不是 "0 / 100k"。
	EstTokensPerCall int `json:"estTokensPerCall,omitempty"`
}

// serverDetail 是详情页所需的全部信息。
type serverDetail struct {
	serverSummary
	Config serverConfigView `json:"config"`
	Tools  []string         `json:"tools,omitempty"` // 下游原始工具名
}

// logItem 是调用日志列表一行。
//
// 持久层的 store.LogEntry 用的是 Go 导出字段名，直接序列化出去
// 会变成 "ID"/"ServerKey" 这种和全 API camelCase 契约不一致的形状。
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

	// 调用方与请求现场（均已在落库时截断）。查看参数与结果
	// 是日志页的核心诉求——"复现这次调用"比统计数字有用得多。
	APIKeyID string `json:"apiKeyId,omitempty"`
	Args     string `json:"args,omitempty"`
	Result   string `json:"result,omitempty"`
}

func toLogItem(e store.LogEntry) logItem {
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
		Args:         e.Args,
		Result:       e.Result,
	}
}

// --- 输入结构 ---

// serverRequest 是新增 / 编辑 server 的请求体。
//
// 语义是整体替换：界面提交的是完整表单，服务端不做"字段级局部更新"。
// 局部更新的边界（空字符串是"不改"还是"清空"）永远说不清，
// 而说不清的边界最后都会变成一个数据被悄悄改掉的 bug。
type serverRequest struct {
	Key       string `json:"key"`
	Name      string `json:"name"`
	Transport string `json:"transport"`

	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`

	// AuthMode：none=无需鉴权，header=需要请求头鉴权，空=按 headers 推断。
	AuthMode string `json:"authMode"`

	Command string   `json:"command"`
	Args    []string `json:"args"`
	Env     []string `json:"env"`

	TimeoutMS       int64  `json:"timeoutMs"`
	ProtocolVersion string `json:"protocolVersion"`
	LegacyOnly      bool   `json:"legacyOnly"`

	ToolFilterMode string   `json:"toolFilterMode"`
	ToolFilterList []string `json:"toolFilterList"`

	// EstTokensPerCall 是 TPM 的预估回退值，0 表示不计量。
	EstTokensPerCall int `json:"estTokensPerCall"`

	// 元数据（PRD §4.7）。
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
	CategoryID  string   `json:"categoryId"`

	// Enabled 为空表示"不指定"：新增时默认启用，编辑时保留原状态。
	Enabled *bool `json:"enabled"`
}

// toConfig 把请求折算成可落库的配置。
//
// prev 是编辑前的配置：新增时为零值。它的用途只有一个——
// 把回显用的掩码换回真实密钥。
func (req serverRequest) toConfig(prev *store.Server) (store.Config, error) {
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
		spec.Headers = mergeSecrets(req.Headers, prev.Spec.Headers)
		spec.Env = mergeEnvSecrets(req.Env, prev.Spec.Env)
	}

	if err := spec.Validate(); err != nil {
		return store.Config{}, badRequest("配置不合法: %v", err)
	}

	mode := store.ToolFilterMode(req.ToolFilterMode).Normalize()
	list := store.CleanToolNames(req.ToolFilterList)
	if err := mode.Validate(list); err != nil {
		return store.Config{}, badRequest("%v", err)
	}

	enabled := true
	if prev != nil {
		enabled = prev.Enabled
	}
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	return store.Config{
		Spec:             spec,
		Enabled:          enabled,
		ToolFilterMode:   mode,
		ToolFilterList:   list,
		EstTokensPerCall: max(req.EstTokensPerCall, 0),
		Description:      strings.TrimSpace(req.Description),
		Tags:             req.Tags,
		CategoryID:       strings.TrimSpace(req.CategoryID),
	}, nil
}

// --- 转换 ---

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
func toSummary(sv store.Server, cachedTools int, exposedTools int, drain *hub.DrainingInfo, keys keyStats, catName string) serverSummary {
	out := serverSummary{
		ID:               sv.Spec.ID,
		Key:              sv.Spec.Key,
		Name:             sv.Spec.Name,
		Transport:        string(sv.Spec.Transport),
		Target:           targetOf(sv.Spec),
		Enabled:          sv.Enabled,
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
		ExposedToolCount: exposedTools,
		ToolFilterMode:   string(sv.ToolFilterMode),
		ToolFilterList:   sv.ToolFilterList,
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
		EstTokensPerCall: sv.EstTokensPerCall,
	}
	applyDraining(&out, drain)
	if sv.Status == "unknown" && sv.LastErrorKind == downstream.KindNone {
		// 从未探活过。区分"没测过"与"测了失败"——前者要提示去测，
		// 后者要提示改配置，两种下一步动作完全不同。
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
func applyDraining(out *serverSummary, drain *hub.DrainingInfo) {
	if drain == nil {
		return
	}
	out.Draining = true
	out.DrainingInflight = drain.Inflight
	out.DrainingSince = drain.StartedAt.UnixMilli()
}

// toDetail 在摘要之上补齐可编辑配置。
func toDetail(sv store.Server, cachedTools int, exposedTools int, toolNames []string, drain *hub.DrainingInfo, keys keyStats, catName string) serverDetail {
	cfg := serverConfigView{
		URL:              sv.Spec.URL,
		Command:          sv.Spec.Command,
		Args:             sv.Spec.Args,
		AuthMode:         sv.Spec.AuthModeOr(),
		TimeoutMS:        sv.Spec.TimeoutOr().Milliseconds(),
		ProtocolVersion:  sv.Spec.ProtocolVersion,
		LegacyOnly:       sv.Spec.LegacyOnly,
		EstTokensPerCall: sv.EstTokensPerCall,
	}
	if len(sv.Spec.Headers) > 0 {
		cfg.Headers = maskMap(sv.Spec.Headers)
		cfg.HeaderKeys = sortedKeys(sv.Spec.Headers)
	}
	if len(sv.Spec.Env) > 0 {
		cfg.Env = maskEnv(sv.Spec.Env)
	}

	return serverDetail{
		serverSummary: toSummary(sv, cachedTools, exposedTools, drain, keys, catName),
		Config:        cfg,
		Tools:         toolNames,
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

// --- 密钥处理 ---

// maskMap 把全部请求头替换成掩码，只保留键名。
//
// 不区分"哪些头算机密"：判断规则一旦写死就会漏。
// 用户仍能看到自己配了哪些头（headerKeys），只是看不到值。
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

// mergeSecrets 把回显用的掩码换回真实值。
//
// 界面拿到的头上是 ***，用户不改它就会把 *** 原样提交回来。
// 直接落库等于"每编辑一次就抹掉一次凭据"——这种坑极难自查，
// 因为界面上一切正常，直到下游开始返回 401。
func mergeSecrets(next, prev map[string]string) map[string]string {
	if len(next) == 0 {
		return next
	}
	out := make(map[string]string, len(next))
	for k, v := range next {
		if v == secretMask {
			if old, ok := prev[k]; ok {
				out[k] = old
				continue
			}
			// 原配置里没有这个键，掩码就是它的真实值——多半是用户
			// 手填了一个字面量 ***。照原样存下去，比丢弃更符合预期。
		}
		out[k] = v
	}
	return out
}

// mergeEnvSecrets 同 mergeSecrets，作用于 KEY=VALUE 列表。
func mergeEnvSecrets(next, prev []string) []string {
	if len(next) == 0 {
		return next
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
