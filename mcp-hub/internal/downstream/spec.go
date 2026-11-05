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

// AuthMode 标识下游天生要不要鉴权。
// 纯展示位，不改变请求行为：发不发鉴权头始终由 headers / 密钥池决定，
// 用途是让界面区分"免鉴权"和"要鉴权但还没配密钥"。
const (
	AuthModeNone   = "none"   // 裸请求即可用
	AuthModeHeader = "header" // 需要请求头鉴权：静态 headers 或密钥池
)

// DefaultTimeout 是下游请求的默认超时。
const DefaultTimeout = 30 * time.Second

// keyPattern 约束工具前缀。下划线留给命名空间分隔符，key 本身不得包含。
var keyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]*$`)

// ReservedMetaPrefix 是网关自带工具的对外名前缀。
// 与 aggregate.MetaPrefix 同值。依赖方向不允许 downstream 反向引用 aggregate，
// 故在此复述；aggregate 侧有用例断言两者恒等。
const ReservedMetaPrefix = "hub__"

// reservedKeys 是不得用作 serverKey 的保留名。
// 判据只有一条：对外名 = <serverKey>__<toolName> 会不会落进保留前缀。
// 因此恰好避开 "hub" 一个（keyPattern 已禁下划线）。
var reservedKeys = map[string]bool{"hub": true}

// Spec 描述一个下游 MCP server 的连接配置。
type Spec struct {
	ID   string `json:"id"`
	Key  string `json:"key"` // 工具前缀，如 github
	Name string `json:"name"`

	Transport Transport `json:"transport"`

	// 远程传输（streamable_http / sse）
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`

	// AuthMode 为空表示按 Headers 推断，见 AuthModeOr。
	AuthMode string `json:"authMode,omitempty"`

	// stdio
	Command string   `json:"command,omitempty"`
	Args    []string `json:"args,omitempty"`
	Env     []string `json:"env,omitempty"`

	Timeout time.Duration `json:"timeout,omitempty"`

	// ProtocolVersion 为空表示自动协商（先新版 server/discover，失败回退旧版）。
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

// IsRemote 报告该 server 是否通过网络访问。
func (s Spec) IsRemote() bool {
	return s.Transport == TransportStreamableHTTP || s.Transport == TransportSSE
}

// AuthModeOr 返回生效的鉴权形态：显式声明优先，未声明时按 Headers 推断。
// 非法值原样透出，由 Validate 拦截。
func (s Spec) AuthModeOr() string {
	switch s.AuthMode {
	case AuthModeNone, AuthModeHeader:
		return s.AuthMode
	case "":
		if len(s.Headers) > 0 {
			return AuthModeHeader
		}
		return AuthModeNone
	}
	return s.AuthMode
}

// ConnFingerprint 概括所有"必须重建连接才生效"的字段。判错代价不对称：该重建而没
// 重建，请求会打到已经不对的目标上；不该重建而重建，会打断在飞调用。
// headers 刻意不在其中（远程凭据按请求注入）；stdio 的 env 是凭据载体，必须重启进程。
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

// Validate 检查配置是否自洽，不触网。
func (s Spec) Validate() error {
	if strings.TrimSpace(s.Key) == "" {
		return errors.New("key is required")
	}
	if !keyPattern.MatchString(s.Key) {
		return fmt.Errorf("key %q must match %s", s.Key, keyPattern)
	}
	// "hub" 是保留命名空间：对外工具名是 <serverKey>__<toolName>，而网关自带
	// 工具的对外名统一以 hub__ 开头。key 取 hub 会让这台 server 的全部工具落进
	// 同一前缀，真撞上 hub__list_tools 等名字时连调用指向哪边都说不清。
	if reservedKeys[strings.ToLower(s.Key)] {
		return fmt.Errorf("key %q 是保留命名空间（网关自带工具用 %s 前缀），请换一个",
			s.Key, ReservedMetaPrefix)
	}

	switch s.AuthMode {
	case "", AuthModeNone, AuthModeHeader:
	default:
		return fmt.Errorf("unknown authMode %q (可选: none / header)", s.AuthMode)
	}
	// stdio 的凭据随子进程环境变量注入，没有"请求头"这回事。
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
