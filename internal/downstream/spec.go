package downstream

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Transport 是下游 server 的接入方式。
type Transport string

const (
	TransportStreamableHTTP Transport = "streamable_http"
	TransportSSE            Transport = "sse"
	TransportStdio          Transport = "stdio"
)

// AuthMode 标识下游 server 的鉴权形态。
//
// 这是个展示用标识位，不改变请求行为——发不发鉴权头始终由
// headers / 密钥池决定（见 Conn.headerFunc）。它回答的是"这个
// 下游天生要不要鉴权"，让界面能区分"免鉴权"和"要鉴权但还没配"：
// 前者不用引导用户去配密钥，后者配了密钥才可用。
const (
	AuthModeNone   = "none"   // 无需鉴权：裸请求即可用
	AuthModeHeader = "header" // 需要请求头鉴权：静态 headers 或密钥池
)

// DefaultTimeout 是下游请求的默认超时。
const DefaultTimeout = 30 * time.Second

// keyPattern 约束工具前缀的字符集。下划线保留给命名空间分隔符，
// 因此 key 本身不得包含连续下划线。
var keyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]*$`)

// Spec 描述一个下游 MCP server 的连接配置。
type Spec struct {
	ID   string `json:"id"`
	Key  string `json:"key"` // 工具前缀，如 github
	Name string `json:"name"`

	Transport Transport `json:"transport"`

	// 远程传输（streamable_http / sse）
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`

	// AuthMode 为空表示按 Headers 自动推断（见 AuthModeOr）。
	AuthMode string `json:"authMode,omitempty"`

	// stdio
	Command string   `json:"command,omitempty"`
	Args    []string `json:"args,omitempty"`
	Env     []string `json:"env,omitempty"`

	Timeout time.Duration `json:"timeout,omitempty"`

	// ProtocolVersion 为空表示自动协商（先新版 server/discover，失败回退旧版 initialize）。
	ProtocolVersion string `json:"protocolVersion,omitempty"`

	// LegacyOnly 强制只走旧版协议，用于确认某 server 确实不支持新版。
	LegacyOnly bool `json:"legacyOnly,omitempty"`
}

// TimeoutOr 返回配置的超时，未配置时返回默认值。
func (s Spec) TimeoutOr() time.Duration {
	if s.Timeout > 0 {
		return s.Timeout
	}
	return DefaultTimeout
}

// ConnFingerprint 概括所有"必须重建连接才能生效"的字段。
//
// 用它判断一次配置变更要不要重建连接。判错的代价不对称：
// 该重建而没重建，请求会打到一个已经不对的目标上，是最难查的那类
// 故障；不该重建而重建了，则会打断正在跑的调用，用户看到"改个名字
// 怎么把别人的任务弄挂了"。因此这里按"确实换目标"来划线。
//
// headers 刻意不在其中：远程传输的凭据是按请求注入的（见 Conn.headerFunc），
// 换密钥只需换掉回调的返回值，不该付重建连接的代价——轮换凭据恰恰是
// 最不该有感知的操作。stdio 的 env 就是凭据载体（子进程启动时注入），
// 改它必须重启进程，所以留在指纹里。
func (s Spec) ConnFingerprint() string {
	var b strings.Builder
	write := func(parts ...string) {
		for _, p := range parts {
			b.WriteString(p)
			b.WriteByte(0)
		}
	}

	write(
		string(s.Transport),
		s.URL,
		s.Command,
		strconv.FormatInt(int64(s.TimeoutOr()), 10),
		s.ProtocolVersion,
		strconv.FormatBool(s.LegacyOnly),
	)
	write(s.Args...)
	write(s.Env...)
	return b.String()
}

// IsRemote 报告该 server 是否通过网络访问。
func (s Spec) IsRemote() bool {
	return s.Transport == TransportStreamableHTTP || s.Transport == TransportSSE
}

// AuthModeOr 返回生效的鉴权形态：显式声明优先，
// 未声明时按 Headers 推断——配了头就是 header，否则 none。
func (s Spec) AuthModeOr() string {
	switch s.AuthMode {
	case AuthModeNone, AuthModeHeader:
		return s.AuthMode
	case "":
		if len(s.Headers) > 0 {
			return AuthModeHeader
		}
		return AuthModeNone
	default:
		return s.AuthMode // 非法值由 Validate 拦截，这里原样透出
	}
}

// Validate 检查配置是否自洽，不触网。
func (s Spec) Validate() error {
	if strings.TrimSpace(s.Key) == "" {
		return errors.New("key is required")
	}
	if !keyPattern.MatchString(s.Key) {
		return fmt.Errorf("key %q must match %s", s.Key, keyPattern)
	}

	switch s.AuthMode {
	case "", AuthModeNone, AuthModeHeader:
	default:
		return fmt.Errorf("unknown authMode %q (可选: none / header)", s.AuthMode)
	}
	// stdio 的凭据随子进程环境变量注入，没有"请求头"的概念，
	// 声明 header 只会在界面上制造一个永远不成立的标识。
	if !s.IsRemote() && s.AuthMode == AuthModeHeader {
		return errors.New("stdio transport 不支持 authMode=header（凭据走 env）")
	}

	switch s.Transport {
	case TransportStreamableHTTP, TransportSSE:
		if s.URL == "" {
			return fmt.Errorf("%s transport requires url", s.Transport)
		}
		u, err := url.Parse(s.URL)
		if err != nil {
			return fmt.Errorf("invalid url: %w", err)
		}
		if u.Host == "" {
			return fmt.Errorf("url %q has no host", s.URL)
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return fmt.Errorf("url scheme must be http or https, got %q", u.Scheme)
		}
	case TransportStdio:
		if strings.TrimSpace(s.Command) == "" {
			return errors.New("stdio transport requires command")
		}
	default:
		return fmt.Errorf("unknown transport %q", s.Transport)
	}
	return nil
}
