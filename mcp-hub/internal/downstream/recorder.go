package downstream

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// bodySnippetBytes 是响应体被截取保留的字节数。
// 只够识别 "insufficient_quota" 这类标记：错误响应体动辄几十 KB，全额缓冲会放大内存。
const bodySnippetBytes = 512

// Observation 是 Recorder 捕获到的一次 HTTP 往返事实。
type Observation struct {
	StatusCode  int
	ContentType string
	Err         error // 未经 mcp-go 包装的原始传输错误
	At          time.Time

	// RetryAfter 是下游在 429/503 上声明的等待时长，已解析。
	// 它比自己猜的指数退避更权威：供应商知道自己什么时候放行。
	RetryAfter time.Duration

	// BodySnippet 是错误响应体的前若干字节，用于区分"限流"与"额度耗尽"。
	// 多数供应商两种情况都回 429，只在 body 里用 insufficient_quota 这类标记区分。
	// 只看状态码会把"该换密钥"误判成"等一会儿就行"，让没钱的密钥被无限退避。
	BodySnippet string
}

// obsSlotKey 是观测槽位在 context 中的键。私有类型，避免与其他包的键冲突。
type obsSlotKey struct{}

// ObservationSlot 承载一次调用的观测结果。
// 密钥池下调用并发：共用"最近一次往返"会把 A 收到的 429 读成 B 的观测，
// 把无辜的密钥封掉；每次调用自带槽位后，归因不再依赖时序。
type ObservationSlot struct {
	mu  sync.Mutex
	obs Observation
	set bool
}

// Get 返回该槽位捕获到的观测。未捕获时为零值。
func (s *ObservationSlot) Get() Observation {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.obs
}

func (s *ObservationSlot) store(obs Observation) {
	s.mu.Lock()
	s.obs = obs
	s.set = true
	s.mu.Unlock()
}

// WithObservation 在 ctx 上挂一个观测槽位。
// 槽位取一次调用内最后一次往返：一次 tools/call 可能经历多次 HTTP 往返
// （协商、重试），决定成败的是最后一次。
func WithObservation(ctx context.Context) (context.Context, *ObservationSlot) {
	slot := &ObservationSlot{}
	return context.WithValue(ctx, obsSlotKey{}, slot), slot
}

// ObservationFrom 取出 ctx 上的观测槽位，没有时返回 nil。
func ObservationFrom(ctx context.Context) *ObservationSlot {
	if ctx == nil {
		return nil
	}
	slot, _ := ctx.Value(obsSlotKey{}).(*ObservationSlot)
	return slot
}

// Recorder 是一个 http.RoundTripper，记录往返的状态码、原始错误与限流信号。
// mcp-go 把状态码与网络错误压成字符串，errors.As 取不回来，而在 RoundTripper
// 这一层它们仍是结构化的。并发安全：有槽位就写槽位，同时更新 last 供串行路径用。
type Recorder struct {
	Base http.RoundTripper

	mu   sync.Mutex
	last Observation
}

// NewRecorder 构造一个带私有连接池的观测 RoundTripper。
// 不能回落到 http.DefaultTransport：其 MaxIdleConnsPerHost 只有 2，低于下游并发上限，
// 一波并发后连接被关，下一波只能重做 TCP/TLS 握手；私有池与并发额度对齐。
func NewRecorder() *Recorder {
	return &Recorder{Base: newHTTPTransport()}
}

// newHTTPTransport 产出与下游并发额度对齐的连接池。
// 从 DefaultTransport 克隆而非手写：TLS 握手超时、Expect-Continue、代理读取等
// 默认值都要保留，只覆盖池容量。
func newHTTPTransport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.MaxIdleConnsPerHost = DefaultMaxInflight
	return t
}

// CloseIdleConnections 立即回收底层连接池中的空闲连接。
// mcp-go 的 client.Close 只关自己的流，不保证回收 Transport 空闲池里的 socket，
// 不主动关就要挂满整个 IdleConnTimeout（默认 90s）。
func (r *Recorder) CloseIdleConnections() {
	if closer, ok := r.Base.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

// RoundTrip 实现 http.RoundTripper。
func (r *Recorder) RoundTrip(req *http.Request) (*http.Response, error) {
	base := r.Base
	if base == nil {
		base = http.DefaultTransport
	}

	resp, err := base.RoundTrip(req)

	obs := Observation{At: time.Now(), Err: err}
	if resp != nil {
		obs.StatusCode = resp.StatusCode
		obs.ContentType = resp.Header.Get("Content-Type")
		obs.RetryAfter = parseRetryAfter(resp.Header.Get("Retry-After"), obs.At)
		obs.BodySnippet = peekErrorBody(resp)
	}

	// 先写槽位再写 last：槽位存在时，调用方要读的必然是它。
	if slot := ObservationFrom(req.Context()); slot != nil {
		slot.store(obs)
	}

	r.mu.Lock()
	r.last = obs
	r.mu.Unlock()

	return resp, err
}

// Last 返回最近一次往返的观测结果。
func (r *Recorder) Last() Observation {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.last
}

// parseRetryAfter 解析 Retry-After 头，秒数与 HTTP-date 都接受（RFC 7231 两种皆可，
// 实际供应商两种都在用）。解析失败返回 0，由调用方退回指数退避，而不是猜一个默认值。
func parseRetryAfter(raw string, now time.Time) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}

	if secs, err := strconv.Atoi(raw); err == nil {
		if secs <= 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}

	if t, err := http.ParseTime(raw); err == nil {
		if d := t.Sub(now); d > 0 {
			return d
		}
		return 0
	}
	return 0
}

// peekErrorBody 读取错误响应的开头若干字节，并把响应体按段透传还原回去。
// 只在状态码 >= 400 且非事件流时读：正常响应可能是 SSE 长连接，缓冲会破坏流式语义，
// 而需要看 body 的判据只出现在错误响应上。不整体读入内存。
func peekErrorBody(resp *http.Response) string {
	if resp.StatusCode < http.StatusBadRequest || resp.Body == nil {
		return ""
	}
	if isEventStream(resp.Header.Get("Content-Type")) {
		return ""
	}

	buf := make([]byte, bodySnippetBytes)
	n, err := io.ReadFull(resp.Body, buf)
	switch {
	case err == nil, err == io.ErrUnexpectedEOF, err == io.EOF:
	default:
		// 读失败：把已读到的部分接回去，别把响应体弄丢。
		if n > 0 {
			resp.Body = newReplayBody(buf[:n], resp.Body)
		}
		return ""
	}

	if n == 0 {
		return ""
	}
	resp.Body = newReplayBody(buf[:n], resp.Body)
	return string(buf[:n])
}

func isEventStream(raw string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(raw)), "text/event-stream")
}

// replayBody 先吐出已缓冲的头部字节，再透传剩余的原始响应体。
type replayBody struct {
	head io.Reader
	rest io.ReadCloser
}

func newReplayBody(head []byte, rest io.ReadCloser) *replayBody {
	return &replayBody{head: bytes.NewReader(head), rest: rest}
}

func (b *replayBody) Read(p []byte) (int, error) {
	if b.head != nil {
		n, err := b.head.Read(p)
		if err == io.EOF {
			b.head = nil
			if n > 0 {
				return n, nil
			}
			return b.rest.Read(p)
		}
		return n, err
	}
	return b.rest.Read(p)
}

func (b *replayBody) Close() error { return b.rest.Close() }
