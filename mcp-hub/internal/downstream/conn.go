package downstream

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"sync"
	"sync/atomic"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
)

// Negotiated 是一次成功协商的结果。
type Negotiated struct {
	ProtocolVersion string
	ServerName      string
	ServerVersion   string
	Instructions    string
	Capabilities    []string
	Legacy          bool
}

// Conn 是与单个下游 server 的常驻连接，负责真正的调用转发。
// 与 Prober 共用 buildClient 与 negotiate：探活通过即调用路径可达；
// 区别是 Prober 每次新建、用完即弃，Conn 长期持有。
type Conn struct {
	spec   Spec
	stderr *stderrSink
	sem    chan struct{} // 并发额度，见 conn_lifecycle.go

	// hdr 是后续请求要带的凭据，hdrVer 是它的版本号。
	// 凭据可原子替换，每个 HTTP 请求通过 headerFunc 现场读取，轮换密钥不必重建连接。
	// 版本号用于 401 重试判定。
	hdr    atomic.Value // map[string]string
	hdrVer atomic.Uint64

	// draining 表示连接已进入停用收尾：拒绝新调用，但不动在飞的那些。
	draining atomic.Bool

	// cancelMu 与 inflightCancels 让强制关闭能真正终止在飞请求，见 conn_lifecycle.go。
	cancelMu        sync.Mutex
	inflightCancels map[uint64]context.CancelFunc
	nextCancelID    uint64

	// lifeCtx 承载连接（尤其是 stdio 子进程）的生命周期，与任何单个请求无关。
	// stdio 传输底层用 exec.CommandContext，传入请求 ctx 会在请求结束时杀掉子进程；
	// 只有 Close 取消它。
	lifeCtx    context.Context
	lifeCancel context.CancelFunc

	mu         sync.RWMutex
	client     *mcpclient.Client
	rec        *Recorder
	negotiated Negotiated
	closed     bool
}

// NewConn 构造一个未连接的下游连接。不触网。
func NewConn(spec Spec) (*Conn, error) {
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	lifeCtx, lifeCancel := context.WithCancel(context.Background())
	c := &Conn{
		spec:            spec,
		stderr:          &stderrSink{},
		sem:             make(chan struct{}, DefaultMaxInflight),
		lifeCtx:         lifeCtx,
		lifeCancel:      lifeCancel,
		inflightCancels: make(map[uint64]context.CancelFunc),
	}
	c.hdr.Store(nonNilHeaders(spec.Headers))
	return c, nil
}

func nonNilHeaders(h map[string]string) map[string]string {
	if h == nil {
		return map[string]string{}
	}
	return h
}

// Spec 返回连接的目标配置。
func (c *Conn) Spec() Spec { return c.spec }

// Dial 建立连接并完成协议协商。幂等：已连接时直接返回缓存结果，不重复握手。
// waitCtx 只限制"愿意等多久"；连接存活期由 Conn 自己掌握（对 stdio 是必须的，
// 见 lifeCtx）。
func (c *Conn) Dial(waitCtx context.Context) (Negotiated, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return Negotiated{}, ErrClosed
	}
	if c.client != nil {
		return c.negotiated, nil
	}

	client, rec, negotiated, err := c.connect(waitCtx)
	if err != nil {
		return Negotiated{}, err
	}

	c.client = client
	c.rec = rec
	c.negotiated = negotiated
	return negotiated, nil
}

// connect 是 Dial 的裸实现，调用方必须持有 c.mu 的写锁。
func (c *Conn) connect(waitCtx context.Context) (*mcpclient.Client, *Recorder, Negotiated, error) {
	rec := NewRecorder()
	client, err := buildClient(c.spec, rec, c.stderr, c.headerFunc())
	if err != nil {
		return nil, nil, Negotiated{}, &CallError{
			Kind:  KindUnknown,
			Stage: StageConnect,
			Err:   fmt.Errorf("构造下游 client 失败: %w", err),
		}
	}

	// Start 放在独立 goroutine 里跑：stdio 的 Start 会一直等到子进程就绪，
	// 命令挂住时连"放弃等待"都做不到。
	startCtx := c.startCtx(waitCtx)
	started := make(chan error, 1)
	go func() { started <- client.Start(startCtx) }()

	select {
	case err := <-started:
		if err != nil {
			client.Close()
			rec.CloseIdleConnections()
			return nil, nil, Negotiated{}, &CallError{
				Kind:   classifyFor(c.spec, err, rec.Last()),
				Stage:  StageConnect,
				Err:    err,
				Stderr: c.stderr.String(),
				Obs:    rec.Last(),
			}
		}

	case <-waitCtx.Done():
		// 放弃等待。Start 可能稍后才返回，再挂一个清理，避免留下已就绪却没人管的子进程。
		go func() {
			<-started
			client.Close()
			rec.CloseIdleConnections()
		}()
		client.Close()
		rec.CloseIdleConnections()
		return nil, nil, Negotiated{}, &CallError{
			Kind:   KindTimeout,
			Stage:  StageConnect,
			Err:    fmt.Errorf("等待下游启动超时: %w", waitCtx.Err()),
			Stderr: c.stderr.String(),
		}
	}

	// 握手沿用 waitCtx：这里的 ctx 只控制等待时长，不会波及已启动的连接。
	negotiated, err := negotiate(waitCtx, client, c.spec)
	if err != nil {
		client.Close()
		rec.CloseIdleConnections()
		return nil, nil, Negotiated{}, &CallError{
			Kind:   classifyFor(c.spec, err, rec.Last()),
			Stage:  StageDiscover,
			Err:    err,
			Stderr: c.stderr.String(),
			Obs:    rec.Last(),
		}
	}

	return client, rec, negotiated, nil
}

// startCtx 返回传输层 Start 使用的 ctx。
// 生命周期必须归 lifeCtx（SSE 事件流、stdio 子进程都不能被单次调用取消），但 Start
// 会从传入 ctx 读凭据，故只把凭据值补进派生 ctx，取消来源仍只有 lifeCtx。
func (c *Conn) startCtx(waitCtx context.Context) context.Context {
	if !c.spec.IsRemote() {
		return c.lifeCtx
	}
	h := credentialHeadersFrom(waitCtx)
	if h == nil {
		return c.lifeCtx
	}
	return context.WithValue(c.lifeCtx, credentialCtxKey{}, h)
}

// ListTools 拉取下游的完整工具清单。
// 新版协议已移除 ping，tools/list 是唯一既确认存活又能刷新缓存的调用，因此同时是
// 健康探针的动作。
func (c *Conn) ListTools(ctx context.Context) ([]mcp.Tool, error) {
	client, err := c.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer c.release()

	callCtx, done := c.beginCall(ctx)
	defer done()
	callCtx, slot := WithObservation(callCtx)

	sentVer := c.hdrVer.Load()
	listed, err := client.ListTools(callCtx, mcp.ListToolsRequest{})
	if err != nil && c.credentialRotated(sentVer, err, slot.Get()) {
		listed, err = client.ListTools(callCtx, mcp.ListToolsRequest{})
	}
	if err != nil {
		return nil, c.wrapWith(err, StageListTools, slot.Get())
	}
	return listed.Tools, nil
}

// CallTool 把一次工具调用转发给下游。
//
// args 用原始 JSON 透传：json.RawMessage 能避免大整数（>2^53）在网关这一跳的精度损失。
func (c *Conn) CallTool(ctx context.Context, name string, args json.RawMessage) (*mcp.CallToolResult, error) {
	client, err := c.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer c.release()

	callCtx, done := c.beginCall(ctx)
	defer done()
	callCtx, slot := WithObservation(callCtx)

	req := mcp.CallToolRequest{}
	req.Params.Name = name
	if len(args) > 0 {
		// 只填 RawArguments：mcp-go 在有 raw 字节时原样发出。若改填 Arguments（any），
		// json.RawMessage 会被当成 []byte 做 base64 编码，参数就废了。
		req.Params.RawArguments = args
	}

	sentVer := c.hdrVer.Load()
	res, err := client.CallTool(callCtx, req)
	if err != nil && c.credentialRotated(sentVer, err, slot.Get()) {
		res, err = client.CallTool(callCtx, req)
	}
	if err != nil {
		return nil, c.wrapWith(err, StageCall, slot.Get())
	}
	return res, nil
}

// credentialRotated 判断这次失败是否该用新凭据重试一次。两个条件缺一不可：失败是
// 鉴权类（401/403 意味着请求未被执行，重试不会让有副作用的工具跑第二遍），且凭据在
// 请求发出后变过（否则是凭据本身不对，重试只会把明确的错误糊成偶发失败）。
func (c *Conn) credentialRotated(sentVer uint64, err error, obs Observation) bool {
	if c.hdrVer.Load() == sentVer {
		return false
	}
	return classifyFor(c.spec, err, obs) == KindAuthRequired
}

// wrapWith 把下游返回的错误转成带分类的 CallError。
// obs 必须来自本次调用自己的观测槽位：密钥池下调用并发，读"最近一次往返"会把 A
// 收到的 429 记到 B 头上，把无辜的密钥封掉。
func (c *Conn) wrapWith(err error, stage Stage, obs Observation) error {
	return &CallError{
		Kind:   classifyFor(c.spec, err, obs),
		Stage:  stage,
		Err:    err,
		Stderr: c.stderr.String(),
		Obs:    obs,
	}
}

// Headers 返回当前生效凭据的副本。
func (c *Conn) Headers() map[string]string {
	return maps.Clone(c.currentHeaders())
}

// SetHeaders 替换后续请求使用的凭据，返回是否真的发生了变化。
// 只影响此后发出的请求，在路上的仍用出发时的凭据。内容相同则不递增版本号：
// 版本号是 401 重试的判据，无谓递增会把明确的配置错误糊成偶发失败。
func (c *Conn) SetHeaders(h map[string]string) bool {
	next := nonNilHeaders(h)
	if maps.Equal(c.currentHeaders(), next) {
		return false
	}
	c.hdr.Store(next)
	c.hdrVer.Add(1)
	return true
}

// currentHeaders 返回当前凭据，供传输层的每个请求调用。
func (c *Conn) currentHeaders() map[string]string {
	if v, ok := c.hdr.Load().(map[string]string); ok {
		return v
	}
	return nil
}

// headerFunc 构造传给传输层的动态凭据回调：本次调用在 ctx 上指定的凭据
// （密钥池选中那一把）优先，否则回落到连接自己那一份。
// 只对远程传输有意义：stdio 的凭据随子进程启动注入环境变量，改它等于重启进程。
func (c *Conn) headerFunc() func(context.Context) map[string]string {
	if !c.spec.IsRemote() {
		return nil
	}
	return func(ctx context.Context) map[string]string {
		if h := credentialHeadersFrom(ctx); h != nil {
			return h
		}
		return c.currentHeaders()
	}
}

// credentialCtxKey 是"本次调用的凭据"在 context 中的键。
type credentialCtxKey struct{}

// WithCredentialHeaders 指定本次调用要注入的凭据。
// 走 ctx 而不是 Conn 字段：并发调用可能同时用着不同的密钥，写进同一字段必然串号。
func WithCredentialHeaders(ctx context.Context, headers map[string]string) context.Context {
	if len(headers) == 0 {
		return ctx
	}
	return context.WithValue(ctx, credentialCtxKey{}, headers)
}

// credentialHeadersFrom 取出 ctx 上指定的凭据，未指定时返回 nil。
func credentialHeadersFrom(ctx context.Context) map[string]string {
	if ctx == nil {
		return nil
	}
	h, _ := ctx.Value(credentialCtxKey{}).(map[string]string)
	return h
}
