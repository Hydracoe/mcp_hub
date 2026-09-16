package downstream

import (
	"encoding/json"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

// ErrorKind 是下游失败的分类。
//
// 分类必须由错误类型推导（errors.Is / errors.As），不靠字符串匹配。
// 唯一例外是"额度耗尽 vs 限流"，供应商只在响应体文案里区分，见 quotaMarkers。
type ErrorKind string

const (
	KindNone ErrorKind = ""

	// L1 网络层
	KindDNSFail     ErrorKind = "DNS_FAIL"
	KindConnRefused ErrorKind = "CONN_REFUSED"
	KindTLSFail     ErrorKind = "TLS_FAIL"
	KindTimeout     ErrorKind = "TIMEOUT"

	// L1 HTTP 语义
	KindAuthRequired  ErrorKind = "AUTH_REQUIRED"
	KindNotFound      ErrorKind = "NOT_FOUND"
	KindUpstreamError ErrorKind = "UPSTREAM_ERROR"
	KindNotMCP        ErrorKind = "NOT_MCP"

	// 限流与配额。单列是因为它们与"下游坏了"本质不同：服务正常，只是这把
	// 凭据不被允许继续打。混进 UNKNOWN，用户对着 429 只会看到"未知错误"，
	// 既不知道该等还是该换密钥。
	KindRateLimited    ErrorKind = "RATE_LIMITED"
	KindQuotaExhausted ErrorKind = "QUOTA_EXHAUSTED"

	// L2 协议层
	KindProtocolMismatch  ErrorKind = "PROTOCOL_MISMATCH"
	KindCapabilityMissing ErrorKind = "CAPABILITY_MISSING"

	// stdio
	KindSpawnFailed      ErrorKind = "SPAWN_FAILED"
	KindExitEarly        ErrorKind = "EXIT_EARLY"
	KindHandshakeTimeout ErrorKind = "HANDSHAKE_TIMEOUT"

	// 生命周期相关：不是下游坏了，而是网关自己的意图导致调用未完成。
	// 混进 UNKNOWN 会让"我主动停用"看起来像"下游故障"。
	KindDraining ErrorKind = "SERVER_DRAINING"
	KindCanceled ErrorKind = "CANCELED"

	// KindNoUsableKey：密钥池里没有一把能用的。既不是下游的错，也不是某一
	// 把密钥的错，但必须有名字——否则用户看到"服务降级"会去查网络，
	// 而真正该查的是密钥与账号。
	KindNoUsableKey ErrorKind = "NO_USABLE_KEY"

	KindUnknown ErrorKind = "UNKNOWN"
)

// humanMessages 给出面向用户的中文解释与排查方向。
var humanMessages = map[ErrorKind]string{
	KindDNSFail:           "域名解析失败：检查 URL 拼写与 DNS",
	KindConnRefused:       "连接被拒绝：目标端口未监听或不可达",
	KindTLSFail:           "TLS 握手失败：证书无效、过期或链不完整",
	KindTimeout:           "连接超时：目标未在超时内响应",
	KindAuthRequired:      "认证失败：缺少或错误的凭据（401/403）",
	KindNotFound:          "路径不存在（404）：检查 MCP 端点路径",
	KindUpstreamError:     "下游服务异常（5xx）：稍后重试或联系提供方",
	KindNotMCP:            "响应不是 MCP 报文：该 URL 可能不是 MCP 端点",
	KindRateLimited:       "被下游限流（429）：等 Retry-After 或退避后重试；配了多把密钥会自动改用下一把",
	KindQuotaExhausted:    "额度或余额已耗尽（402 / 429 声明额度不足）：这把密钥需要更换或充值",
	KindProtocolMismatch:  "协议不兼容：新旧协议协商均失败",
	KindCapabilityMissing: "服务可达但不提供 tools 能力",
	KindSpawnFailed:       "命令无法启动：可执行文件不存在或无执行权限",
	KindExitEarly:         "进程启动后立即退出：见 stderr 输出",
	KindHandshakeTimeout:  "进程存活但未响应协议：可能不是 MCP server",
	KindDraining:          "该 server 正在停用，已不接受新调用",
	KindCanceled:          "调用被中断：server 已停用且超过收尾等待上限",
	KindNoUsableKey:       "没有可用密钥：全部处于冷却或已被弃用，检查密钥配置与账号额度",
	KindUnknown:           "未知错误",
}

// Human 返回该分类的中文说明。
func (k ErrorKind) Human() string {
	if msg, ok := humanMessages[k]; ok {
		return msg
	}
	return string(k)
}

// Stage 标记探活停留在哪一步。
type Stage string

const (
	StageNetwork   Stage = "network"
	StageConnect   Stage = "connect"
	StageDiscover  Stage = "discover"
	StageListTools Stage = "list_tools"
	StageCall      Stage = "call"
)

// ProbeResult 是一次探活的完整结果。
type ProbeResult struct {
	OK bool `json:"ok"`

	// 协商结果，写入 servers 表缓存，后续调用直接沿用，不必每次试错两次。
	ProtocolVersion string   `json:"protocolVersion,omitempty"`
	ServerName      string   `json:"serverName,omitempty"`
	ServerVersion   string   `json:"serverVersion,omitempty"`
	Instructions    string   `json:"instructions,omitempty"`
	Capabilities    []string `json:"capabilities,omitempty"`
	Legacy          bool     `json:"legacy,omitempty"` // 协商到旧版协议

	ToolCount int        `json:"toolCount"`
	Tools     []ToolInfo `json:"tools,omitempty"`

	// 分段耗时，用于定位"慢在哪一段"。
	Latency StageLatency `json:"latency"`

	// 失败信息
	Stage     Stage     `json:"stage,omitempty"`
	Kind      ErrorKind `json:"kind,omitempty"`
	Detail    string    `json:"detail,omitempty"`
	Stderr    string    `json:"stderr,omitempty"` // 仅 stdio
	CheckedAt time.Time `json:"checkedAt"`

	// HTTP 观测。带上它们是为了让密钥判决复用探活结论，探活与在线调用
	// 看到同一套判据，界面才不会自相矛盾。
	StatusCode  int           `json:"statusCode,omitempty"`
	RetryAfter  time.Duration `json:"retryAfter,omitempty"`
	BodySnippet string        `json:"-"`
}

// ToolInfo 是探活时抓到的工具定义，用于填充聚合工具缓存。
type ToolInfo struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
}

// StageLatency 记录各阶段的耗时，单位毫秒。
type StageLatency struct {
	Network   int64 `json:"networkMs"`
	Connect   int64 `json:"connectMs"`
	Discover  int64 `json:"discoverMs"`
	ListTools int64 `json:"listToolsMs"`
	Total     int64 `json:"totalMs"`
}

// ToolsToInfo 把下游工具定义压成可持久化的形态。
func ToolsToInfo(tools []mcp.Tool) []ToolInfo {
	out := make([]ToolInfo, 0, len(tools))
	for _, t := range tools {
		out = append(out, ToolInfo{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: marshalSchema(t.InputSchema),
		})
	}
	return out
}

// marshalSchema 序列化工具入参 schema，失败返回 nil（不影响探活结论）。
//
// 保真度边界：走的是 mcp-go 的结构化往返，顶层 oneOf / anyOf / $ref 这类
// 关键字不保留（properties 内部是 map[string]any，不受影响）。要做到逐字节
// 透传得自己发原始 JSON-RPC，约 200 行协议代码，收益只在罕见的入参形态上。
func marshalSchema(s mcp.ToolInputSchema) json.RawMessage {
	b, err := json.Marshal(s)
	if err != nil {
		return nil
	}
	return b
}

// fail 就地标记探活失败。
func (r *ProbeResult) fail(stage Stage, kind ErrorKind, err error) {
	r.OK = false
	r.Stage = stage
	r.Kind = kind
	if err != nil {
		r.Detail = err.Error()
	}
}

// capture 把一次 HTTP 观测中与判决相关的字段带进结果。
//
// 只带三样：状态码区分 401/403，Retry-After 是退避的权威来源，body 片段
// 区分"限流"与"额度耗尽"。其余观测字段对用户没有意义。
func (r *ProbeResult) capture(obs Observation) {
	r.StatusCode = obs.StatusCode
	r.RetryAfter = obs.RetryAfter
	r.BodySnippet = obs.BodySnippet
}

// Observation 把探活结果折算成一次 HTTP 观测，供密钥判决复用。
func (r *ProbeResult) Observation() Observation {
	return Observation{
		StatusCode:  r.StatusCode,
		RetryAfter:  r.RetryAfter,
		BodySnippet: r.BodySnippet,
	}
}
