package downstream

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
)

// classify 把一次失败的探活归入 ErrorKind。
//
// 判定顺序即诊断顺序：先看是否超时，再看网络层，再看 HTTP 语义，
// 最后才落到协议层。这样"网络不通"不会被误报成"协议不兼容"。
func classify(err error, obs Observation) ErrorKind {
	if err == nil {
		return KindNone
	}

	if errors.Is(err, context.DeadlineExceeded) {
		return KindTimeout
	}

	// 被主动取消：停用收尾超时后强制关闭会走到这里。
	// 单列一类是为了让日志里能一眼看出"是我们关的"，而不是下游的毛病——
	// 混进 UNKNOWN 的话，用户只会看到一堆"未知错误"，
	// 完全无从把它和几分钟前那次停用操作联系起来。
	if errors.Is(err, context.Canceled) {
		return KindCanceled
	}

	if kind := classifyNetwork(err); kind != KindNone {
		return kind
	}

	// HTTP 语义。只在响应确实异常时采信状态码：探活是串行的，Recorder
	// 记录的"最近一次"即失败的那一次；若状态码是 2xx，说明失败发生在
	// 响应体处理阶段，应交给下面的协议层判断。
	if obs.Err == nil && obs.StatusCode >= http.StatusBadRequest {
		switch {
		case obs.StatusCode == http.StatusTooManyRequests:
			// 429 有两种截然不同的含义，只有 body 能区分它们。
			// 判错的代价不对称：把额度耗尽当成限流，会让一把已经
			// 没钱的密钥被无限退避重试；反过来则会把一把还能用的
			// 密钥直接弃用。
			if quotaExhaustedIn(obs.BodySnippet) {
				return KindQuotaExhausted
			}
			return KindRateLimited
		case obs.StatusCode == http.StatusPaymentRequired:
			return KindQuotaExhausted
		case obs.StatusCode == http.StatusUnauthorized, obs.StatusCode == http.StatusForbidden:
			return KindAuthRequired
		case obs.StatusCode == http.StatusNotFound:
			return KindNotFound
		case obs.StatusCode >= http.StatusInternalServerError:
			return KindUpstreamError
		}
	}

	// 2xx 但内容类型不是 MCP 支持的格式——典型场景是把普通网页地址
	// 当成 MCP 端点填了进来：状态码 200，返回的却是 HTML。
	// mcp-go 对这种情况只给出字符串错误，因此改在这里用结构化的
	// Content-Type 判断，避免依赖错误文案。
	if obs.Err == nil &&
		obs.StatusCode >= http.StatusOK && obs.StatusCode < http.StatusMultipleChoices &&
		!isMCPContentType(obs.ContentType) {
		return KindNotMCP
	}

	var unsupported mcp.UnsupportedProtocolVersionError
	if errors.As(err, &unsupported) {
		return KindProtocolMismatch
	}
	if errors.Is(err, mcp.ErrMethodNotFound) {
		return KindProtocolMismatch
	}

	// 服务返回了 HTTP 200，但响应体不是合法 JSON-RPC——
	// 典型场景是把普通网页地址当成 MCP 端点填了进来。
	var syntaxErr *json.SyntaxError
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &syntaxErr) || errors.As(err, &typeErr) {
		return KindNotMCP
	}

	return KindUnknown
}

// quotaMarkers 是各家供应商在"额度/余额用尽"时给出的响应体标记。
//
// 这是一处刻意保留的字符串匹配：供应商的额度信号**只**存在于响应体
// 文案里，协议层没有对应的结构化字段。判定逻辑集中在此，一旦某家
// 改了措辞，只需在这里加一条，不必去动调度逻辑。
//
// 匹配一律转小写后按子串找。少了会退化成"无限退避重试"，
// 多了会把额度耗尽的密钥当成限流——因此只放明确指向"没钱/额度"的词，
// 不放过 is not available 这类含糊说法。
var quotaMarkers = []string{
	"insufficient_quota",
	"quota exceeded",
	"exceeded your current quota",
	"quota_exhausted",
	"out of credits",
	"credit balance is too low",
	"insufficient balance",
	"insufficient_balance",
	"no remaining quota",
	"billing hard limit",
	"余额不足",
	"额度不足",
	"额度已用尽",
	"配额不足",
}

// quotaExhaustedIn 报告响应体是否声明了额度耗尽。
func quotaExhaustedIn(body string) bool {
	if body == "" {
		return false
	}
	lower := strings.ToLower(body)
	for _, marker := range quotaMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// isMCPContentType 报告该 Content-Type 是否为 MCP 传输可用的类型。
//
// Streamable HTTP 只在两种媒体类型下承载 JSON-RPC：
// application/json 与 text/event-stream。
// 空值不做判断——部分 server 不声明，交给后续解析定夺。
func isMCPContentType(raw string) bool {
	if raw == "" {
		return true
	}
	mediaType, _, err := mime.ParseMediaType(raw)
	if err != nil {
		return false
	}
	switch mediaType {
	case "application/json", "text/event-stream":
		return true
	}
	return false
}

// classifyNetwork 识别网络与 TLS 层的错误。
func classifyNetwork(err error) ErrorKind {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return KindDNSFail
	}

	var certVerifyErr *tls.CertificateVerificationError
	if errors.As(err, &certVerifyErr) {
		return KindTLSFail
	}
	var recordErr tls.RecordHeaderError
	if errors.As(err, &recordErr) {
		// 连上了端口，但对端回的不是 TLS 握手数据——多半是 HTTP 端口。
		return KindTLSFail
	}
	var unknownAuthority x509.UnknownAuthorityError
	if errors.As(err, &unknownAuthority) {
		return KindTLSFail
	}
	var hostnameErr x509.HostnameError
	if errors.As(err, &hostnameErr) {
		return KindTLSFail
	}
	var certInvalid x509.CertificateInvalidError
	if errors.As(err, &certInvalid) {
		return KindTLSFail
	}

	if errors.Is(err, syscall.ECONNREFUSED) {
		return KindConnRefused
	}
	if errors.Is(err, context.DeadlineExceeded) || os.IsTimeout(err) {
		return KindTimeout
	}
	// 对端直接关闭连接，通常说明这个地址不提供 MCP 服务。
	if errors.Is(err, syscall.ECONNRESET) {
		return KindNotMCP
	}

	return KindNone
}

// classifySpawn 识别 stdio 子进程启动失败的原因。
func classifySpawn(err error) ErrorKind {
	if err == nil {
		return KindNone
	}
	if errors.Is(err, context.Canceled) {
		return KindCanceled
	}

	var execErr *exec.Error
	if errors.As(err, &execErr) {
		return KindSpawnFailed
	}
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		switch {
		case errors.Is(pathErr.Err, syscall.ENOENT),
			errors.Is(pathErr.Err, syscall.EACCES),
			errors.Is(pathErr.Err, syscall.EPERM):
			return KindSpawnFailed
		}
	}
	if errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM) {
		return KindSpawnFailed
	}
	// 握手期间管道断开：进程几乎立刻死了。
	if errors.Is(err, transport.ErrTransportClosed) ||
		errors.Is(err, io.EOF) ||
		errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, os.ErrClosed) {
		return KindExitEarly
	}

	return KindUnknown
}
