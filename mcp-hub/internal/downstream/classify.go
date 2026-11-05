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

// classifyFor 按传输类型选分类器。stdio 没有网络层与 HTTP 语义，失败原因
// 来自进程生命周期，不能复用网络分类器。
func classifyFor(spec Spec, err error, obs Observation) ErrorKind {
	if spec.Transport == TransportStdio {
		return classifySpawn(err)
	}
	return classify(err, obs)
}

// lastObs 取观测器的最近一次往返，rec 为 nil 时返回零值。
func lastObs(rec *Recorder) Observation {
	if rec == nil {
		return Observation{}
	}
	return rec.Last()
}

// classify 把一次失败归入 ErrorKind。
// 判定顺序即诊断顺序：超时 → 网络层 → HTTP 语义 → 协议层，避免网络不通被误报成协议不兼容。
func classify(err error, obs Observation) ErrorKind {
	if err == nil {
		return KindNone
	}

	if errors.Is(err, context.DeadlineExceeded) {
		return KindTimeout
	}
	// 被主动取消：停用收尾超时后强制关闭会走到这里；单列一类，日志里才能
	// 一眼看出是我们关的。
	if errors.Is(err, context.Canceled) {
		return KindCanceled
	}

	if kind := classifyNetwork(err); kind != KindNone {
		return kind
	}

	// HTTP 语义。只在响应确实异常时采信状态码：2xx 说明失败发生在响应体处理阶段，
	// 交给下面的协议层判断。
	if obs.Err == nil && obs.StatusCode >= http.StatusBadRequest {
		switch {
		case obs.StatusCode == http.StatusTooManyRequests:
			// 429 有两种含义，只有 body 能区分。判错的代价不对称：把额度耗尽
			// 当成限流会让没钱的密钥被无限退避重试，反过来会把还能用的密钥弃用。
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

	// 2xx 但不是 MCP 支持的媒体类型——典型是把普通网页地址填成了 MCP 端点。
	// mcp-go 对这种情况只给字符串错误，只能靠结构化的 Content-Type 判断。
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

	// 200 但响应体不是合法 JSON-RPC，同样是"这地址不是 MCP 端点"。
	var syntaxErr *json.SyntaxError
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &syntaxErr) || errors.As(err, &typeErr) {
		return KindNotMCP
	}

	return KindUnknown
}

// quotaMarkers 是各家在"额度/余额用尽"时给出的响应体标记。
// 全包唯一一处字符串匹配：额度信号只在响应体文案里，协议层没有对应字段。
// 只放明确指向"没钱/额度"的词——少了会无限退避重试，多了会把额度耗尽当成限流。
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
// Streamable HTTP 只在 application/json 与 text/event-stream 下承载 JSON-RPC；
// 空值交给后续解析定夺（部分 server 不声明）。
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
	// 连上了端口，但对端回的不是 TLS 握手数据——多半是 HTTP 端口。
	var recordErr tls.RecordHeaderError
	if errors.As(err, &recordErr) {
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
