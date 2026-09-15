package downstream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"sync"
	"sync/atomic"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
)

// ErrClosed 表示连接已被主动关闭，不再接受调用。
var ErrClosed = errors.New("downstream 连接已关闭")

// ErrDraining 表示连接正在停用，已不接受新调用。
var ErrDraining = errors.New("downstream 连接正在停用")

// DefaultMaxInflight 是单个下游允许的并发调用上限。
//
// 存在的理由：一个卡住的下游不应该把网关的 goroutine 全部占满。
// 超出上限的调用会先排队，再由 ctx 决定是等待还是放弃。
const DefaultMaxInflight = 16

// drainPollInterval 是 Drain 检查在飞调用的间隔。
//
// 用轮询而不是条件变量：Drain 是低频的管理操作（用户点一下停用），
// 每次最多浪费几十个 tick，换来的是不必维护一套唤醒机制——
// 而唤醒机制出错时的症状（等不到在飞归零）恰好是最难排查的那种。
const drainPollInterval = 20 * time.Millisecond

// Negotiated 是一次成功协商的结果。
type Negotiated struct {
	ProtocolVersion string
	ServerName      string
	ServerVersion   string
	Instructions    string
	Capabilities    []string
	Legacy          bool
}

// Conn 是与单个下游 server 的常驻连接。
//
// 与 Prober 的分工：Prober 每次新建、用完即弃，用于回答"这个配置能用吗"；
// Conn 长期持有，用于真正的调用转发。两者共用同一套 buildClient 与
// negotiate 路径，因此探活通过就意味着调用路径可达。
//
// 并发安全：streamable_http 下每个请求都是一次独立的 HTTP 往返，
// mcp-go 的 requestID 是原子的，因此 CallTool 可并发。并发上限由 sem 控制。
type Conn struct {
	spec   Spec
	stderr *stderrSink
	sem    chan struct{}

	// hdr 是后续请求要带的凭据，hdrVer 是它的版本号。
	//
	// 凭据独立于 spec 存放且可原子替换，是为了让轮换密钥不必重建连接：
	// 每个 HTTP 请求通过 headerFunc 现场读取，改了下一毫秒就生效，
	// 已在路上的那次调用不受任何影响。版本号用于判断"这次鉴权失败
	// 是不是因为凭据刚被换过"，见 credentialRotated。
	hdr    atomic.Value // map[string]string
	hdrVer atomic.Uint64

	// draining 表示连接已进入停用收尾：拒绝新调用，但不动在飞的那些。
	draining atomic.Bool

	// cancelMu 与 inflightCancels 让强制关闭能真正终止在飞请求。
	//
	// 光把连接从池里摘掉并不够：已经发出的 HTTP 请求有它自己的生命周期，
	// 不取消就会一直等下去。取消它们，才是"强制关闭"这个词唯一
	// 有意义的解释——否则停用会卡在界面上永远转圈。
	cancelMu        sync.Mutex
	inflightCancels map[uint64]context.CancelFunc
	nextCancelID    uint64

	// lifeCtx 承载连接（尤其是 stdio 子进程）的生命周期，与任何单个
	// 请求都无关。stdio 传输底层用的是 exec.CommandContext，传进去的
	// ctx 一取消子进程就被杀——若把某个请求的 ctx 传进去，那个请求
	// 结束时下游就没了，下一次调用只能看到一个已死的连接。
	// 只有 Close 才取消它。
	lifeCtx    context.Context
	lifeCancel context.CancelFunc

	mu         sync.RWMutex
	client     *mcpclient.Client
	rec        *Recorder
	negotiated Negotiated
	openedAt   time.Time
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

// Dial 建立连接并完成协议协商。
//
// 幂等：已连接时直接返回缓存结果，不重复握手。协商结果被缓存下来，
// 后续调用不再经历"先试新版、失败再降级"的两次往返。
//
// waitCtx 只用于限制"愿意等多久"。连接本身的存活期由 Conn 自己掌握，
// 不受调用方 ctx 影响——这一点对 stdio 是必须的，见 lifeCtx 的说明。
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
	c.openedAt = time.Now()
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

	// Start 在独立 goroutine 里跑：stdio 的 Start 会一直等到子进程
	// 就绪，如果命令挂住，我们不能连"放弃等待"都做不到。
	started := make(chan error, 1)
	go func() { started <- client.Start(c.lifeCtx) }()

	select {
	case err := <-started:
		if err != nil {
			client.Close()
			rec.CloseIdleConnections()
			return nil, nil, Negotiated{}, &CallError{
				Kind:   c.classify(err, rec),
				Stage:  StageConnect,
				Err:    err,
				Stderr: c.stderr.String(),
				Obs:    rec.Last(),
			}
		}

	case <-waitCtx.Done():
		// 放弃等待。Start 可能稍后才返回，因此再挂一个清理：
		// 等它结束后立刻关掉，避免留下一个已就绪却没人管的子进程。
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

	// 握手沿用 waitCtx：单次请求的 ctx 在这里只控制等待时长，
	// 不会波及已启动的连接。
	negotiated, err := negotiate(waitCtx, client, c.spec)
	if err != nil {
		client.Close()
		rec.CloseIdleConnections()
		return nil, nil, Negotiated{}, &CallError{
			Kind:   c.classify(err, rec),
			Stage:  StageDiscover,
			Err:    err,
			Stderr: c.stderr.String(),
			Obs:    rec.Last(),
		}
	}

	return client, rec, negotiated, nil
}

// Negotiated 返回缓存的协商结果。未连接时返回零值。
func (c *Conn) Negotiated() Negotiated {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.negotiated
}

// OpenedAt 返回连接建立时刻。未连接时为零值。
func (c *Conn) OpenedAt() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.openedAt
}

// ListTools 拉取下游的完整工具清单。
//
// 这是健康探针的动作——新版协议已移除 ping，tools/list 是唯一
// 既确认存活又能顺带刷新缓存的调用。
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
// args 使用原始 JSON 透传：参数可能在网关这一跳被重新序列化，
// 而 json.RawMessage 能避免大整数（>2^53）的精度损失。
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
		// 只填 RawArguments：mcp-go 的 CallToolParams.MarshalJSON 在有 raw
		// 字节时直接原样发出。若改填 Arguments（any），json.RawMessage 会被
		// 当成 []byte 做 base64 编码，参数就废了。Raw 路径也顺带避免了
		// 大整数越过 2^53 时的精度损失。
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

// credentialRotated 判断这次失败是否该用新凭据重试一次。
//
// 两个条件缺一不可：
//
//   - 失败是鉴权类（401/403）。这类响应意味着请求**没有被执行**，
//     所以重试不会让有副作用的工具跑第二遍——这是敢重试的全部前提。
//     超时与 5xx 可能已经执行过，绝不能进入这条路径。
//   - 请求发出之后凭据版本变过。否则就是凭据本身不对，重试只是多打
//     一次无效请求，还会把"密钥错了"这个明确信号糊成"偶发失败"。
//
// 落空的窗口正是最要紧的那个：调用出发时用的是旧密钥，往返期间运维
// 换了新密钥，下游若已吊销旧密钥就会回 401。没有这条重试，调用方
// 会在轮换的一瞬间看到一次失败。
//
// 密钥池模式下这条路径不会被触发（凭据按调用选，hdrVer 不动），
// 那次 401 由池子按 KeyVerdict 处理：弃用该密钥、换下一把。
func (c *Conn) credentialRotated(sentVer uint64, err error, obs Observation) bool {
	if c.hdrVer.Load() == sentVer {
		return false
	}
	return c.classifyObs(err, obs) == KindAuthRequired
}

// acquire 取出一份可用的 client 引用，并占用一个并发额度。
//
// 未连接时先尝试连接：连接池是懒建的，调用路径不应因为"探活还没跑完"
// 而失败。连接失败即以结构化错误返回，不能让调用方陷入等待。
func (c *Conn) acquire(ctx context.Context) (*mcpclient.Client, error) {
	c.mu.RLock()
	closed := c.closed
	client := c.client
	c.mu.RUnlock()

	if closed {
		return nil, ErrClosed
	}
	// 停用收尾期间不再接新调用，但在飞的那些照常跑完。
	// "拒绝新来的"与"不打断已有的"是同一个决定的两面。
	if c.draining.Load() {
		return nil, &CallError{Kind: KindDraining, Stage: StageCall, Err: ErrDraining}
	}
	if client == nil {
		if _, err := c.Dial(ctx); err != nil {
			return nil, err
		}
		c.mu.RLock()
		client = c.client
		c.mu.RUnlock()
	}
	if client == nil {
		return nil, &CallError{Kind: KindUnknown, Stage: StageConnect, Err: ErrClosed}
	}

	select {
	case c.sem <- struct{}{}:
		return client, nil
	case <-ctx.Done():
		return nil, &CallError{Kind: KindTimeout, Stage: StageCall, Err: ctx.Err()}
	}
}

// release 归还并发额度。
func (c *Conn) release() { <-c.sem }

// beginCall 为一次调用准备可取消的上下文，并登记到在飞集合。
//
// 登记是为了让 Close 能撤销它。把连接从池里摘掉并不等于调用会停下来——
// 已经发出的 HTTP 请求有它自己的生命周期。没有这一步，"等到上限就强关"
// 只是一句空话：连接关了，调用还挂着，界面上的"停用中"永远不会结束。
func (c *Conn) beginCall(ctx context.Context) (context.Context, func()) {
	callCtx, cancel := context.WithCancel(ctx)

	c.cancelMu.Lock()
	c.nextCancelID++
	id := c.nextCancelID
	c.inflightCancels[id] = cancel
	c.cancelMu.Unlock()

	return callCtx, func() {
		c.cancelMu.Lock()
		delete(c.inflightCancels, id)
		c.cancelMu.Unlock()
		cancel()
	}
}

// cancelInflight 取消全部在飞调用。只在关闭连接时调用。
func (c *Conn) cancelInflight() {
	c.cancelMu.Lock()
	cancels := make([]context.CancelFunc, 0, len(c.inflightCancels))
	for _, fn := range c.inflightCancels {
		cancels = append(cancels, fn)
	}
	c.inflightCancels = make(map[uint64]context.CancelFunc)
	c.cancelMu.Unlock()

	for _, fn := range cancels {
		fn()
	}
}

// Inflight 返回当前正在执行的调用数。
//
// 直接读 sem 的占用数，不另设计数器：多一个计数器就多一处可能与它
// 不同步的状态，而在飞数算错会直接导致停用等错时机。
func (c *Conn) Inflight() int { return len(c.sem) }

// IsDraining 报告连接是否已进入停用收尾。
func (c *Conn) IsDraining() bool { return c.draining.Load() }

// Headers 返回当前生效凭据的副本。
func (c *Conn) Headers() map[string]string {
	return maps.Clone(c.currentHeaders())
}

// SetHeaders 替换后续请求使用的凭据，返回是否真的发生了变化。
//
// 只影响此后发出的请求：已经在路上的那一次仍用它出发时的凭据。
// 这正是想要的效果——轮换密钥不该去动一个已经发出去的请求。
//
// 内容相同则不递增版本号。版本号是 401 重试的判据，无谓的递增
// 会让"凭据没变却触发了重试"发生，把明确的配置错误糊成偶发失败。
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

// headerFunc 构造传给传输层的动态凭据回调。
//
// 取值顺序：本次调用在 ctx 上指定的凭据（密钥池选中那一把）优先，
// 否则回落到连接自己的那一份（单密钥模式）。这条优先级让两种模式
// 共用一条传输路径，单密钥模式的行为与改造前逐字节一致。
//
// 只对远程传输有意义：stdio 的凭据随子进程启动一次性注入环境变量，
// 改它必须重启进程，做不到按请求更新，因此不支持。
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
//
// 走 ctx 而不是 Conn 上的字段，是因为密钥池要为**每一次调用**挑一把
// 密钥：并发调用可能同时用着不同的密钥，把它们写进同一个字段必然串号。
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

// Drain 停止接受新调用，等在飞调用自然结束，然后关闭连接。
//
// 等待期间不打断任何已发出的请求——这是"优雅停用"的全部意义。
// ctx 决定愿意等多久；到期返回 ctx.Err()，是否强关留给调用方判断，
// 因为"等多久算太久"是策略，不该由连接层决定。
func (c *Conn) Drain(ctx context.Context) error {
	c.draining.Store(true)

	ticker := time.NewTicker(drainPollInterval)
	defer ticker.Stop()

	for {
		if c.Inflight() == 0 {
			return c.Close()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// wrapWith 把下游返回的错误转成带分类的 CallError。
//
// obs 必须来自**本次调用自己的观测槽位**，不能用"最近一次往返"。
// 探活是串行的，所以单槽一直是对的；但密钥池下调用并发，读最近一次
// 会把 A 收到的 429 记到 B 头上，症状是把无辜的密钥封掉——
// 而且看起来像是供应商的错，几乎无法从日志回溯。
func (c *Conn) wrapWith(err error, stage Stage, obs Observation) error {
	return &CallError{
		Kind:   c.classifyObs(err, obs),
		Stage:  stage,
		Err:    err,
		Stderr: c.stderr.String(),
		Obs:    obs,
	}
}

func (c *Conn) recorder() *Recorder {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.rec
}

// classify 按传输类型选择分类器。stdio 没有网络层与 HTTP 语义，
// 失败原因来自进程生命周期，不能复用网络分类器。
func (c *Conn) classify(err error, rec *Recorder) ErrorKind {
	return c.classifyObs(err, rec.Last())
}

func (c *Conn) classifyObs(err error, obs Observation) ErrorKind {
	if c.spec.Transport == TransportStdio {
		return classifySpawn(err)
	}
	return classify(err, obs)
}

// Close 关闭连接并拒绝后续调用。可重复调用。
func (c *Conn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return nil
	}
	c.closed = true

	// 取消在飞调用。正常收尾（Drain 等到归零）时这里没有东西可取消；
	// 只有超时强关与 Drop 才会走到，而那时不取消就等于没关。
	c.cancelInflight()

	// 取消生命周期 ctx 会连带回收 stdio 子进程，
	// 即使 client.Close 因故没能完整清理。
	defer c.lifeCancel()

	if c.client == nil {
		return nil
	}
	err := c.client.Close()
	// client.Close 不回收 Transport 空闲池里的 socket，
	// 主动关掉，避免旧下游的连接挂满 90s 的 IdleConnTimeout。
	if c.rec != nil {
		c.rec.CloseIdleConnections()
	}
	c.client = nil
	c.rec = nil
	if err != nil {
		return fmt.Errorf("关闭下游连接失败: %w", err)
	}
	return nil
}

// CallError 是带分类的下游调用错误。
//
// 网关对外必须把"下游为什么失败"讲清楚，而不是抛一个裸 error——
// 上层据此决定 HTTP 状态码、日志字段与给 AI 的可读说明。
type CallError struct {
	Kind   ErrorKind
	Stage  Stage
	Err    error
	Stderr string // 仅 stdio

	// Obs 是本次调用自己的 HTTP 观测（状态码、Retry-After、响应体片段）。
	//
	// 带着它走，是为了让上层的密钥调度不必再去猜：判决要看状态码
	// 与响应体内容，而这些信息在错误被 mcp-go 包装成字符串后就没了。
	Obs Observation
}

func (e *CallError) Error() string {
	if e.Err == nil {
		return string(e.Kind)
	}
	return fmt.Sprintf("%s: %v", e.Kind, e.Err)
}

// Unwrap 让 errors.Is / errors.As 能穿透到原始错误。
func (e *CallError) Unwrap() error { return e.Err }

// CallErrorKind 从任意错误中提取分类，取不到时返回 KindUnknown。
func CallErrorKind(err error) ErrorKind {
	var ce *CallError
	if errors.As(err, &ce) {
		return ce.Kind
	}
	// 密钥池打空不是"下游坏了"，但它有明确分类。漏掉这一步会让用户
	// 看到"未知错误"，把一段本可执行的说明（还有几把在冷却、约多久
	// 后重试）降级成一句无用的"稍后重试即可"。
	var noKey *ErrNoUsableKey
	if errors.As(err, &noKey) {
		return KindNoUsableKey
	}
	return KindUnknown
}

// CallErrorObservation 从任意错误中提取本次调用的 HTTP 观测。
func CallErrorObservation(err error) Observation {
	var ce *CallError
	if errors.As(err, &ce) {
		return ce.Obs
	}
	return Observation{}
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
