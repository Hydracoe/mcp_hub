package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/hydra/mcp-hub/internal/downstream"
	"github.com/mark3labs/mcp-go/mcp"
	"go.uber.org/zap"
)

// 退避参数。下游挂掉后，网关不应把每次调用都变成一次完整的连接尝试——
// 那会让"下游已经不可用"这件事从 1ms 的快速失败退化成 30s 的超时。
const (
	backoffInitial = 2 * time.Second
	backoffMax     = 30 * time.Second
)

// DefaultDrainTimeout 是停用时等待在飞调用收尾的上限。
//
// 为什么要有上限：调用可能永远不返回（下游卡死、网络黑洞），没有上限
// 就意味着连接永远关不掉，管理界面上那个 server 会一直显示"停用中"，
// 而用户没有任何办法。1 分钟是个折中——绝大多数正常调用远短于此，
// 而真卡住的那些，多等也没有意义。
const DefaultDrainTimeout = time.Minute

// UnavailableError 表示目标下游处于退避窗口内，本次调用未真正发起。
//
// 携带 RetryAfter 让上层能给出"多久后重试"的明确指引，
// 而不是让调用方自己猜。
type UnavailableError struct {
	ServerKey  string
	Kind       downstream.ErrorKind
	Detail     string
	RetryAfter time.Duration
}

func (e *UnavailableError) Error() string {
	return fmt.Sprintf("下游 %s 暂不可用(%s)，%s 后重试: %s",
		e.ServerKey, e.Kind, e.RetryAfter.Round(time.Second), e.Detail)
}

// MaxKeyAttemptsDefault 是单次调用内最多尝试几把密钥。
//
// 上限 3 是权衡：密钥池的目的是摊薄与容错，不是无限重试。5 把密钥
// 全试一遍意味着一次调用最坏要等 5 个下游超时，而调用方只会看到
// 一个"很慢"。真正的容量问题该由配额与告警解决，不是靠重试。
const MaxKeyAttemptsDefault = 3

// Pool 为每个 server 维护一条常驻连接。
//
// 与 Registry 的分工：Registry 决定"这次调用该发给谁"，Pool 负责
// "怎么把话递过去"。Pool 是唯一持有下游连接的地方。
type Pool struct {
	mu    sync.RWMutex
	conns map[string]*pooled
	fail  map[string]*failure

	// retiring 是已经摘出池子、正在等收尾的连接。
	// 单独记一份是为了让管理接口能回答"停用完成了吗，还有几个调用在跑"。
	retiring map[string]*retirement

	// rings 是每 server 的密钥池。为空表示该 server 走单密钥模式
	// （沿用 spec.Headers），行为与引入密钥池之前完全一致。
	ringMu sync.RWMutex
	rings  map[string]*downstream.KeyRing

	// MaxAttempts 覆盖默认的调用内重试预算。0 取 min(可用密钥数, 3)。
	MaxAttempts int

	// OnKeyEvent 在密钥状态发生变化时被调用。
	//
	// 用回调而不是让 Pool 直接写库：连接池不该知道存储的存在，
	// 否则"连接怎么管"与"状态怎么存"两件事会互相纠缠，
	// 测试也就没法只跑其中一半。
	OnKeyEvent func(KeyEvent)

	// DrainTimeout 是等一次收尾的上限。零值取 DefaultDrainTimeout。
	DrainTimeout time.Duration

	log *zap.Logger

	// 可注入以求可测
	now func() time.Time
}

// KeyEvent 是密钥状态的一次变化，供上层落库与记日志。
type KeyEvent struct {
	ServerID  string
	ServerKey string
	KeyID     string
	KeyLabel  string
	Verdict   downstream.KeyVerdict
	State     downstream.KeyState
	Cooling   time.Duration
	Disabled  bool
	Recovered bool
	Detail    string

	// CollectiveProtection 表示"本会被弃用，但因集体失败保护而未弃用"。
	// 这个信号意味着问题多半在配置或账号层面，而不是密钥本身。
	CollectiveProtection bool

	At time.Time
}

// pooled 是一条连接及其配置指纹。
type pooled struct {
	conn *downstream.Conn

	// spec 与 fingerprint 记录这条连接是按哪份配置建起来的。
	// 指纹用于判断配置变更要不要重建连接——它不含 headers，
	// 因此轮换凭据不会走到重建这条路上。
	spec        downstream.Spec
	fingerprint string

	// dialMu 让同一 server 的并发首次连接只发生一次，
	// 避免 16 个并发请求触发 16 次握手。
	dialMu sync.Mutex
}

// retirement 是一次连接收尾的过程。
//
// 它把"连接"和"这次收尾从什么时候开始"绑在一起，供管理接口
// 估算还剩多久（Deadline）以及是否已经超出等待上限。
type retirement struct {
	serverID  string
	serverKey string
	conn      *downstream.Conn
	startedAt time.Time
	deadline  time.Time
	done      chan struct{}
}

// DrainingInfo 是一条正在收尾的连接的对外快照。
type DrainingInfo struct {
	ServerID  string    `json:"serverId"`
	ServerKey string    `json:"serverKey"`
	Inflight  int       `json:"inflight"`
	StartedAt time.Time `json:"startedAt"`
	Deadline  time.Time `json:"deadline"`
}

// failure 记录连续失败与退避窗口。
type failure struct {
	count  int
	until  time.Time
	kind   downstream.ErrorKind
	detail string
}

// NewPool 构造空连接池。
func NewPool(logger *zap.Logger) *Pool {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Pool{
		conns:    make(map[string]*pooled),
		fail:     make(map[string]*failure),
		retiring: make(map[string]*retirement),
		rings:    make(map[string]*downstream.KeyRing),
		log:      logger,
		now:      time.Now,
	}
}

// --- 密钥池 ---

// SyncKeyRing 让某个 server 的密钥池与一份最新的持久快照对齐。
//
// 已存在的池子走增量 Sync，**保留冷却与计数**：把 label 改一下就
// 顺手清掉冷却，等于给用户开了一个"点一下即可绕过限流"的后门。
//
// creds 为空表示退回单密钥模式（用 servers.headers）。这条回退
// 是刻意的：密钥池是可选增强，只填一把密钥的用户不该感知到任何差别。
func (p *Pool) SyncKeyRing(serverID string, opts downstream.KeyRingOptions, creds []downstream.CredentialInput) {
	p.ringMu.Lock()
	defer p.ringMu.Unlock()

	if len(creds) == 0 {
		delete(p.rings, serverID)
		return
	}
	if existing, ok := p.rings[serverID]; ok {
		existing.Sync(creds)
		return
	}
	p.rings[serverID] = downstream.NewKeyRing(opts, creds)
}

// DropKeyRing 移除某个 server 的密钥池（server 被删除时）。
func (p *Pool) DropKeyRing(serverID string) {
	p.ringMu.Lock()
	delete(p.rings, serverID)
	p.ringMu.Unlock()
}

// KeyRing 返回某个 server 的密钥池，单密钥模式下为 nil。
func (p *Pool) KeyRing(serverID string) *downstream.KeyRing {
	p.ringMu.RLock()
	defer p.ringMu.RUnlock()
	return p.rings[serverID]
}

// KeyRingFor 返回某个 server 的密钥池，附带规格用于展示。
func (p *Pool) KeyRingFor(spec downstream.Spec) *downstream.KeyRing {
	return p.KeyRing(spec.ID)
}

// KeyStatuses 返回全部 server 的密钥运行状态，键为 serverId。
func (p *Pool) KeyStatuses() map[string][]downstream.KeyStatus {
	p.ringMu.RLock()
	defer p.ringMu.RUnlock()

	out := make(map[string][]downstream.KeyStatus, len(p.rings))
	for id, ring := range p.rings {
		out[id] = ring.Status()
	}
	return out
}

// KeyStatusesOf 返回某个 server 的密钥运行状态。
func (p *Pool) KeyStatusesOf(serverID string) []downstream.KeyStatus {
	ring := p.KeyRing(serverID)
	if ring == nil {
		return nil
	}
	return ring.Status()
}

// attemptsFor 计算本次调用的密钥尝试预算。
func (p *Pool) attemptsFor(ring *downstream.KeyRing) int {
	if p.MaxAttempts > 0 {
		return p.MaxAttempts
	}
	n := ring.UsableCount()
	switch {
	case n < 1:
		return 1 // 一把都不可用时也要走一次，才能拿到那个带 retryAfter 的错误
	case n > MaxKeyAttemptsDefault:
		return MaxKeyAttemptsDefault
	}
	return n
}

// emitKeyEvent 把密钥状态变化交给上层。
func (p *Pool) emitKeyEvent(spec downstream.Spec, lease *downstream.KeyLease, out downstream.KeyOutcome) {
	if p.OnKeyEvent == nil || lease == nil {
		return
	}
	// 只有真正发生变化的才上报：每次成功都回调会把日志刷满，
	// 而"成功"这件事本来就由调用日志记录。
	if !out.Disabled && out.Cooling <= 0 && !out.CollectiveProtection && !out.Recovered {
		return
	}
	p.OnKeyEvent(KeyEvent{
		ServerID:             spec.ID,
		ServerKey:            spec.Key,
		KeyID:                lease.ID(),
		KeyLabel:             out.KeyLabel,
		Verdict:              out.Verdict,
		State:                out.State,
		Cooling:              out.Cooling,
		Disabled:             out.Disabled,
		Recovered:            out.Recovered,
		CollectiveProtection: out.CollectiveProtection,
		Detail:               out.Detail,
		At:                   p.now(),
	})
}

// drainTimeout 返回收尾等待上限。
func (p *Pool) drainTimeout() time.Duration {
	if p.DrainTimeout > 0 {
		return p.DrainTimeout
	}
	return DefaultDrainTimeout
}

// Get 取出（必要时建立）某个 server 的连接。
//
// 处于退避窗口内时快速失败，不发任何网络请求。
func (p *Pool) Get(ctx context.Context, spec downstream.Spec) (*downstream.Conn, error) {
	if err := spec.Validate(); err != nil {
		return nil, err
	}

	fp := spec.ConnFingerprint()

	p.mu.Lock()
	if f, ok := p.fail[spec.ID]; ok && p.now().Before(f.until) {
		remaining := f.until.Sub(p.now())
		p.mu.Unlock()
		return nil, &UnavailableError{
			ServerKey:  spec.Key,
			Kind:       f.kind,
			Detail:     f.detail,
			RetryAfter: remaining,
		}
	}

	// 池里那条连接如果是按另一份配置建的，就不能再用它发请求。
	// 正常路径上 Sync 已经淘汰过它了，这里是兜底：任何绕过 Sync 的
	// 写路径都不该让请求打到旧目标上——那是最难查的一类故障，
	// 表现是一切正常，实际全发去了上一个环境。
	var evicted *downstream.Conn
	entry, ok := p.conns[spec.ID]
	if ok && entry.fingerprint != fp {
		evicted = entry.conn
		entry = nil
		delete(p.conns, spec.ID)
	}

	if entry == nil {
		conn, err := downstream.NewConn(spec)
		if err != nil {
			p.mu.Unlock()
			return nil, err
		}
		entry = &pooled{conn: conn, spec: spec, fingerprint: fp}
		p.conns[spec.ID] = entry
	}
	p.mu.Unlock()

	if evicted != nil {
		p.retireConn(spec.ID, spec.Key, evicted)
	}

	entry.dialMu.Lock()
	_, err := entry.conn.Dial(ctx)
	entry.dialMu.Unlock()

	if err != nil {
		kind := downstream.CallErrorKind(err)
		// 密钥级的失败（401/403/429/402）不该让整个 server 进入退避：
		// 那是某一组凭据的处境，换一把就过去了。让 server 退避会连累
		// 池子里其他还健康的密钥，把"换一把即可"变成"整个下游不可用"。
		verdict := downstream.VerdictOf(kind, downstream.CallErrorObservation(err))
		if !verdict.Retryable() {
			p.recordFailure(spec.ID, kind, err.Error(), kind == downstream.KindTimeout)
		}
		return nil, err
	}

	p.recordSuccess(spec.ID)
	return entry.conn, nil
}

// CallTool 通过连接池转发一次工具调用。
//
// 配了密钥池时走"选密钥 → 调用 → 按判决重试"的预算循环；否则就是
// 一次调用、一次机会。重试的边界很窄：只有确定**未被执行**的信号
// （401/402/403/429）才换密钥重试，超时与 5xx 一律不重试——
// MCP 的工具调用没有标准幂等键，盲目重试可能让有副作用的工具
// 执行两次，而重复下单比一次失败严重得多。
func (p *Pool) CallTool(
	ctx context.Context,
	spec downstream.Spec,
	toolName string,
	args json.RawMessage,
) (*mcp.CallToolResult, error) {
	started := time.Now()
	var (
		credentialID string
		attempts     int
	)
	res, err := runWithKeys(p, ctx, spec,
		func(callCtx context.Context, lease *downstream.KeyLease) (*mcp.CallToolResult, int, error) {
			if lease != nil {
				credentialID = lease.ID()
			}
			attempts++
			conn, err := p.Get(callCtx, spec)
			if err != nil {
				return nil, -1, err
			}
			res, err := conn.CallTool(callCtx, toolName, args)
			if err != nil {
				return nil, -1, err
			}
			return res, downstream.ExtractTokenUsage(res), nil
		})

	// 每次调用一行日志：排障时"哪个 server、哪个工具、用的哪把密钥、
	// 试了几次、多久"是第一个要回答的问题。密钥事件另有专门日志，
	// 这里只记结果。
	fields := []zap.Field{
		zap.String("server", spec.Key),
		zap.String("tool", toolName),
		zap.Int("attempts", attempts),
		zap.Int64("ms", time.Since(started).Milliseconds()),
	}
	if credentialID != "" {
		fields = append(fields, zap.String("credential", credentialID))
	}
	if err != nil {
		fields = append(fields,
			zap.String("kind", string(downstream.CallErrorKind(err))),
			zap.Error(err))
		p.log.Warn("工具调用失败", fields...)
	} else {
		p.log.Info("工具调用完成", fields...)
	}
	return res, err
}

// ListTools 通过连接池拉取某个 server 的工具清单。
//
// 与 CallTool 共用同一条密钥选择与重试路径：探活也是真实的调用，
// 限流同样会打到它，让两条路径行为一致才不会出现"调用能过、
// 刷新工具却报 429"这种难以解释的现象。
func (p *Pool) ListTools(ctx context.Context, spec downstream.Spec) ([]mcp.Tool, error) {
	tools, err := runWithKeys(p, ctx, spec,
		func(callCtx context.Context, _ *downstream.KeyLease) ([]mcp.Tool, int, error) {
			conn, err := p.Get(callCtx, spec)
			if err != nil {
				return nil, -1, err
			}
			out, err := conn.ListTools(callCtx)
			if err != nil {
				return nil, -1, err
			}
			return out, -1, nil
		})
	if err != nil {
		return nil, err
	}
	return tools, nil
}

// runWithKeys 是密钥池下的统一执行路径。
//
// 写成包级泛型函数而不是方法：Go 的方法不能带类型参数，
// 而这里的逻辑对 tools/call 与 tools/list 完全相同。
func runWithKeys[T any](
	p *Pool,
	ctx context.Context,
	spec downstream.Spec,
	op func(ctx context.Context, lease *downstream.KeyLease) (T, int, error),
) (T, error) {
	var zero T

	ring := p.KeyRing(spec.ID)
	if ring == nil {
		// 单密钥模式：一次调用、一次机会。这是回归底线——
		// 只填一把密钥的用户不该感知到任何差别。
		res, _, err := op(ctx, nil)
		if err != nil {
			p.noteFailure(spec, err, downstream.VerdictNeutral)
			return zero, err
		}
		p.recordSuccess(spec.ID)
		return res, nil
	}

	budget := p.attemptsFor(ring)
	var (
		excluded map[string]bool
		lastErr  error
	)

	for attempt := 0; attempt < budget; attempt++ {
		lease, err := ring.Acquire(excluded)
		if err != nil {
			// 一把都借不到。若本轮之前已经失败过，回报那个失败原因——
			// 它比"没有可用密钥"更接近用户想知道的答案（比如全在冷却，
			// 而冷却正是因为刚才那几次 429）。
			if lastErr != nil {
				return zero, lastErr
			}
			return zero, err
		}

		callCtx := downstream.WithCredentialHeaders(ctx, lease.Headers())
		res, tokens, err := op(callCtx, lease)
		if err == nil {
			lease.Done(downstream.LeaseResult{
				Verdict:      downstream.VerdictOK,
				ActualTokens: tokens,
			})
			p.recordSuccess(spec.ID)
			p.noteAttempt(ctx, lease.ID(), attempt+1)
			return res, nil
		}

		lastErr = err
		obs := downstream.CallErrorObservation(err)
		verdict := downstream.VerdictOf(downstream.CallErrorKind(err), obs)
		p.emitKeyEvent(spec, lease, lease.Done(downstream.LeaseResult{
			Verdict:      verdict,
			Obs:          obs,
			ActualTokens: -1,
		}))
		p.noteAttempt(ctx, lease.ID(), attempt+1)

		if !verdict.Retryable() {
			p.noteFailure(spec, err, verdict)
			return zero, err
		}

		// 每次重试必须换密钥：同一把再打一次毫无意义，还会加重限流。
		if excluded == nil {
			excluded = make(map[string]bool, budget)
		}
		excluded[lease.ID()] = true
	}

	// 预算耗尽。分两种情形：
	//
	//   - 池子里还有别的可用密钥（只是本次预算用完了）：返回**最后一次**
	//     的失败原因。具体的"下游限流了"比"没有可用密钥"更接近事实。
	//   - 池子已经被这几轮失败打空（全在冷却 / 全被弃用）：返回
	//     ErrNoUsableKey。它带着 retryAfter 和各状态计数，调用方能据此
	//     告诉用户"约 30 秒后重试"，而"RATE_LIMITED"只会让人干等着猜。
	//
	// 探测时**不带 exclude**：刚失败的那几把此刻已是冷却或弃用状态，
	// 把它们排除在外只会让冷却计数变成 0——而那正是用户最想知道的一件事。
	p.log.Warn("调用内密钥重试预算耗尽",
		zap.String("server", spec.Key), zap.Int("attempts", budget), zap.Error(lastErr))

	if noKey := ring.Unavailable(nil); noKey != nil {
		noKey.Cause = lastErr
		p.log.Warn("无可用密钥",
			zap.String("server", spec.Key),
			zap.Int("total", noKey.Total),
			zap.Int("cooling", noKey.Cooling),
			zap.Int("exhausted", noKey.Exhausted),
			zap.Int("disabled", noKey.Disabled),
			zap.Duration("retryAfter", noKey.RetryAfter),
			zap.Error(noKey.Cause))
		return zero, noKey
	}
	return zero, lastErr
}

// noteAttempt 把"这次用了哪把密钥、第几次尝试"交给调用方的日志。
func (p *Pool) noteAttempt(ctx context.Context, credentialID string, attempt int) {
	if meta := callMetaFrom(ctx); meta != nil {
		meta.record(credentialID, attempt)
	}
}

// noteFailure 记录一次与密钥无关的失败。
//
// 密钥级的判决（限流、无效、额度耗尽）不进这里：那是某一把密钥的
// 处境，不该让整个 server 进入退避窗口——否则一把密钥被限流会连累
// 池子里其他还健康的密钥一起被快速失败。
func (p *Pool) noteFailure(spec downstream.Spec, err error, verdict downstream.KeyVerdict) {
	if verdict.Retryable() {
		return
	}
	// 传输层断裂说明这条长连接已经死了（下游重启、LB 空闲回收），
	// 置为失效，下一次调用会重新握手。
	if isConnLost(err) {
		p.Drop(spec.ID)
		return
	}
	kind := downstream.CallErrorKind(err)
	p.recordFailure(spec.ID, kind, err.Error(), kind == downstream.KindTimeout)
}

// Drop 立即关闭并移除某条连接。
//
// 只在确实要立刻停止服务时用（进程退出、连接已确证损坏）。
// 配置变更、停用、删除都该走 Retire——用户点一下后台开关，
// 不该把别人正在跑的任务一起干掉。
func (p *Pool) Drop(serverID string) {
	p.mu.Lock()
	entry := p.conns[serverID]
	delete(p.conns, serverID)
	delete(p.fail, serverID)
	p.mu.Unlock()

	if entry != nil {
		entry.conn.Close()
	}
}

// Retire 让一条连接优雅退场：先从池中摘除，再等它在飞调用跑完才关。
//
// 摘除是立刻的——从这一刻起没有新调用会走到它上面。等的是已经在跑
// 的那些：它们不该因为运维在后台点了一下开关就被打断。有副作用的
// 工具（写文件、下单、发消息）被拦腰截断，可能留下不一致的状态。
//
// 返回值仅用于日志与测试；收尾在后台 goroutine 里进行，不阻塞调用方。
func (p *Pool) Retire(serverID string) {
	p.mu.Lock()
	entry := p.conns[serverID]
	delete(p.conns, serverID)
	delete(p.fail, serverID)
	p.mu.Unlock()

	if entry == nil {
		return
	}
	p.retireConn(serverID, entry.spec.Key, entry.conn)
}

// retireConn 接手一条已从池中摘除的连接，后台等它收尾。
func (p *Pool) retireConn(serverID, serverKey string, conn *downstream.Conn) {
	limit := p.drainTimeout()

	// 没有在飞调用时不必登记：收尾会瞬间完成，
	// 让它在管理接口上闪一下"停用中"反而是噪音。
	if inflight := conn.Inflight(); inflight > 0 {
		r := &retirement{
			serverID:  serverID,
			serverKey: serverKey,
			conn:      conn,
			startedAt: p.now(),
			deadline:  p.now().Add(limit),
			done:      make(chan struct{}),
		}
		p.mu.Lock()
		p.retiring[serverID] = r
		p.mu.Unlock()

		p.log.Info("连接进入收尾，等待在飞调用结束",
			zap.String("server", serverKey), zap.Int("inflight", inflight), zap.Duration("limit", limit))
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), limit)
		defer cancel()

		err := conn.Drain(ctx)

		p.mu.Lock()
		r := p.retiring[serverID]
		delete(p.retiring, serverID)
		p.mu.Unlock()
		if r != nil {
			close(r.done)
		}

		if err != nil {
			// 等到上限还没收完：调用多半卡在下游了。强关是唯一出路，
			// 但要留下明确痕迹——不然那些调用会以"未知错误"收场，
			// 没人能把它们和这次停用联系起来。
			still := conn.Inflight()
			p.log.Warn("连接收尾超过等待上限，强制关闭",
				zap.String("server", serverKey), zap.Duration("waited", limit), zap.Int("inflight", still),
				zap.String("reason", "等待在飞调用超时，这些调用会被标记为 CANCELED"))
			_ = conn.Close()
			return
		}
		p.log.Info("连接已收尾关闭", zap.String("server", serverKey))
	}()
}

// Draining 返回正在收尾的连接，供管理接口展示停用进度。
func (p *Pool) Draining() []DrainingInfo {
	p.mu.RLock()
	defer p.mu.RUnlock()

	out := make([]DrainingInfo, 0, len(p.retiring))
	for _, r := range p.retiring {
		out = append(out, DrainingInfo{
			ServerID:  r.serverID,
			ServerKey: r.serverKey,
			Inflight:  r.conn.Inflight(),
			StartedAt: r.startedAt,
			Deadline:  r.deadline,
		})
	}
	return out
}

// DrainingOf 返回某个 server 的收尾状态，无收尾时返回 false。
func (p *Pool) DrainingOf(serverID string) (DrainingInfo, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	r, ok := p.retiring[serverID]
	if !ok {
		return DrainingInfo{}, false
	}
	return DrainingInfo{
		ServerID:  r.serverID,
		ServerKey: r.serverKey,
		Inflight:  r.conn.Inflight(),
		StartedAt: r.startedAt,
		Deadline:  r.deadline,
	}, true
}

// UpdateCredentials 把新凭据交给现有连接，从下一个请求起生效。
//
// 返回是否找到了可更新的连接。找不到也不算问题：下次建连会直接用
// 最新配置，凭据本来就是按请求注入的。
//
// 这是"轮换密钥调用方无感"的落点——连接不重建，在飞调用不受影响，
// 后续请求自动带着新凭据出去。
func (p *Pool) UpdateCredentials(serverID string, headers map[string]string) bool {
	p.mu.RLock()
	entry := p.conns[serverID]
	p.mu.RUnlock()

	if entry == nil {
		return false
	}
	// stdio 的凭据在子进程的环境变量里，改它等于换进程，
	// 只能靠 ConnFingerprint 判定重建，不能走这条热更新路径。
	if !entry.spec.IsRemote() {
		return false
	}
	return entry.conn.SetHeaders(headers)
}

// Close 关闭全部连接。
func (p *Pool) Close() error {
	p.mu.Lock()
	entries := make([]*pooled, 0, len(p.conns))
	for _, e := range p.conns {
		entries = append(entries, e)
	}
	p.conns = make(map[string]*pooled)
	p.fail = make(map[string]*failure)
	p.mu.Unlock()

	p.ringMu.Lock()
	p.rings = make(map[string]*downstream.KeyRing)
	p.ringMu.Unlock()

	var firstErr error
	for _, e := range entries {
		if err := e.conn.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Sync 让连接池与一组最新的 spec 对齐：消失的连接退场、目标变更的重建。
//
// 这里只处理连接生命周期，不改动 Registry——两件事分开做，
// 避免配置变更与聚合快照刷新互相阻塞。
//
// 判据是连接指纹而不是"配置变了没有"：改过滤名单、改显示名、换密钥
// 都不该动连接，只有真换了目标（url/command/env/超时/协议版本）才重建。
// 这是"改个名字怎么把别人的任务弄挂了"这类问题的根治点。
func (p *Pool) Sync(specs []downstream.Spec) {
	want := make(map[string]downstream.Spec, len(specs))
	for _, s := range specs {
		want[s.ID] = s
	}

	p.mu.RLock()
	var gone, changed []string
	for id, entry := range p.conns {
		spec, ok := want[id]
		if !ok {
			gone = append(gone, id)
			continue
		}
		if entry.fingerprint != spec.ConnFingerprint() {
			changed = append(changed, id)
		}
	}
	p.mu.RUnlock()

	// 两条路径都走优雅退场：配置变更不是打断在飞调用的理由。
	for _, id := range gone {
		p.Retire(id)
	}
	for _, id := range changed {
		p.Retire(id)
	}

	// 清掉已消失 server 的密钥池。池子里存着密钥明文，
	// 让它们随 server 一起消失，而不是留在内存里等着被翻出来。
	p.ringMu.Lock()
	for id := range p.rings {
		if _, ok := want[id]; !ok {
			delete(p.rings, id)
		}
	}
	p.ringMu.Unlock()
}

// Failure 返回某个 server 当前的失败状态，无失败时返回零值。
func (p *Pool) Failure(serverID string) (count int, until time.Time, ok bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	f, exists := p.fail[serverID]
	if !exists {
		return 0, time.Time{}, false
	}
	return f.count, f.until, true
}

// recordSuccess 清空失败计数。
func (p *Pool) recordSuccess(serverID string) {
	p.mu.Lock()
	delete(p.fail, serverID)
	p.mu.Unlock()
}

// recordFailure 累加失败并推进退避窗口。
//
// timeout 不立即进入退避：单个慢工具会把整条连接误判为不可用，
// 而慢不等于坏。它只累加计数，留给后续失败决策。
func (p *Pool) recordFailure(serverID string, kind downstream.ErrorKind, detail string, timeout bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	f := p.fail[serverID]
	if f == nil {
		f = &failure{}
		p.fail[serverID] = f
	}
	f.count++
	f.kind = kind
	f.detail = detail
	if timeout && f.count < 3 {
		f.until = time.Time{}
		return
	}

	wait := backoffInitial << min(f.count-1, 4)
	if wait > backoffMax {
		wait = backoffMax
	}
	f.until = p.now().Add(wait)
}

// isConnLost 判断错误是否意味着底层连接已不可用。
//
// 只认传输层断裂信号。这些情况下请求多半没有送达，
// 因此重建连接是安全的。
func isConnLost(err error) bool {
	if errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, downstream.ErrClosed) {
		return true
	}
	// stdio 进程退出也会让连接不可用，分类器已把它归到 EXIT_EARLY。
	return downstream.CallErrorKind(err) == downstream.KindExitEarly
}
