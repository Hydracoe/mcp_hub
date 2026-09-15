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
//
// 只够识别 "insufficient_quota" / "exceeded your current quota" 这类标记。
// 保留更多没有意义：这些响应体动辄几十 KB，全额缓冲会把它变成
// 一次内存放大攻击的入口。
const bodySnippetBytes = 512

// Observation 是 Recorder 捕获到的一次 HTTP 往返事实。
type Observation struct {
	StatusCode  int
	ContentType string
	Err         error // 未经 mcp-go 包装的原始传输错误
	At          time.Time

	// RetryAfter 是下游在 429/503 上声明的等待时长，已解析。
	//
	// 它比自己猜的指数退避更权威：供应商知道自己什么时候会放行，
	// 网关瞎猜只会要么白等、要么再撞一次限流。
	RetryAfter time.Duration

	// BodySnippet 是错误响应体的前若干字节，用于区分"限流"与"额度耗尽"。
	//
	// 多数供应商这两种情况都回 429，只在 body 里用 insufficient_quota
	// 这类标记区分。仅凭状态码判断会把"该换密钥"误判成"等一会儿就行"，
	// 于是一把已经没钱的密钥会被无限退避重试。
	BodySnippet string
}

// obsSlotKey 是观测槽位在 context 中的键。用私有类型避免与其他
// 包可能写入的键冲突。
type obsSlotKey struct{}

// ObservationSlot 承载一次调用的观测结果。
//
// 存在的理由：Recorder 原先只保留"最近一次往返"，在探活这种串行
// 场景下一直是对的。但密钥池下调用是并发的——A 收到的 429 会被读成
// B 的观测结果，症状是把无辜的密钥封掉，而且看起来像供应商的错。
// 改为每次调用自带一个槽位，归因就不再依赖时序。
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

// Captured 报告是否真的捕获到了一次往返。
func (s *ObservationSlot) Captured() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.set
}

func (s *ObservationSlot) store(obs Observation) {
	s.mu.Lock()
	s.obs = obs
	s.set = true
	s.mu.Unlock()
}

// WithObservation 在 ctx 上挂一个观测槽位。
//
// 层数取一次调用内最后一次往返：一次 tools/call 在传输层可能经历
// 多次 HTTP 往返（协商、重试），最终决定成败的是最后一次。
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

// Recorder 是一个 http.RoundTripper，记录往返的状态码、原始错误与
// 限流信号。
//
// 存在的理由：mcp-go 把 HTTP 状态码与网络错误压成字符串
// （transport/streamable_http.go 中 "request failed with status %d"），
// errors.As 取不到状态码。而错误必须按类型分类，见 ErrorKind 的约束。
// 在 RoundTripper 这一层，状态码与 *url.Error 都还是结构化的。
//
// 并发安全：若请求的 ctx 上挂了观测槽位（WithObservation），结果写进
// 槽位，互不干扰；同时仍更新 last 供探活这类串行路径使用。
type Recorder struct {
	Base http.RoundTripper

	mu   sync.Mutex
	last Observation
}

// NewRecorder 构造一个带私有连接池的观测 RoundTripper。
//
// 不能让它回落 http.DefaultTransport：默认 MaxIdleConnsPerHost 只有 2，
// 而单个下游的并发上限是 DefaultMaxInflight（16）。一波 16 并发过后
// 只有 2 条连接能进空闲池，剩下的全部关闭，下一波只能重新做
// TCP/TLS 握手——高并发下延迟与握手开销都被无谓放大。
//
// 每个下游一个私有 Transport：空闲池容量与并发额度对齐，停用时
// 也能整池 CloseIdleConnections 回收，不与其他下游串扰。
func NewRecorder() *Recorder {
	return &Recorder{Base: newHTTPTransport()}
}

// newHTTPTransport 产出与下游并发额度对齐的连接池。
//
// 从 DefaultTransport 克隆而不是手写一份：TLS 握手超时、
// Expect-Continue、代理读取这些默认值都要保留，只覆盖池容量。
func newHTTPTransport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.MaxIdleConnsPerHost = DefaultMaxInflight
	return t
}

// CloseIdleConnections 立即回收底层连接池中的空闲连接。
//
// 连接停用或探活结束时调用：mcp-go 的 client.Close 只关自己的流，
// 不保证回收 Transport 空闲池里的 socket，不主动关它们就要挂满
// 整个 IdleConnTimeout（默认 90s）。
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

// Reset 清空观测记录，在一轮探活开始前调用。
func (r *Recorder) Reset() {
	r.mu.Lock()
	r.last = Observation{}
	r.mu.Unlock()
}

// parseRetryAfter 解析 Retry-After 头，同时接受秒数与 HTTP-date。
//
// 两种格式都要认：RFC 7231 允许两种，实际供应商两种都在用。
// 解析失败返回 0（由调用方退回指数退避），而不是某个默认值——
// 猜一个时长比不猜更糟，因为它会让"下游说 30s 后好"被改成别的数字。
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

// peekErrorBody 读取错误响应的开头若干字节，并把响应体还原回去。
//
// 只在状态码 >= 400 且非事件流时读：正常响应可能是 SSE 长连接，
// 缓冲它会直接破坏流式语义，而需要看 body 的判据（限流 vs 额度耗尽）
// 本来就只出现在错误响应上。
//
// 还原用 MultiReader 而不是全部读入内存：后者会把这个观察点
// 变成一次无上限的内存分配。
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
