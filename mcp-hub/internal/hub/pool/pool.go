// Package pool 是唯一持有下游连接的地方：常驻连接、懒建连、失败退避、
// 密钥池调度与优雅退场。它不知道 MCP 端点长什么样，也不写库：
// 密钥状态变化通过 OnKeyEvent 回调交给上层落库与记日志。
package pool

import (
	"fmt"
	"sync"
	"time"

	"github.com/hydra/mcp-hub/internal/downstream"
	"go.uber.org/zap"
)

// 退避参数。下游挂掉后不该把每次调用都变成一次完整连接尝试，
// 否则"下游已不可用"会从 1ms 快速失败退化成 30s 超时。
const (
	backoffInitial = 2 * time.Second
	backoffMax     = 30 * time.Second
)

// DefaultDrainTimeout 是停用时等待在飞调用收尾的上限。
//
// 必须设上限：调用可能永远不返回（下游卡死、网络黑洞），连接就永远关不掉，
// server 会一直停在"停用中"。1 分钟足够覆盖正常调用。
const DefaultDrainTimeout = time.Minute

// UnavailableError 表示目标下游处于退避窗口内，本次调用未真正发起。
// 携带 RetryAfter，上层才能给出"多久后重试"的明确指引。
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

// Pool 为每个 server 维护一条常驻连接，是唯一持有下游连接的地方。
// 分工：Registry 决定"该发给谁"，Pool 负责"怎么把话递过去"。
type Pool struct {
	mu    sync.RWMutex
	conns map[string]*pooled
	fail  map[string]*failure

	// retiring 是已经摘出池子、正在等收尾的连接，供管理接口展示停用进度。
	// 键是每次退役的唯一编号而不是 serverId：同一 server 可有多条连接同时收尾，
	// 按 serverId 记会互相覆盖并误关别人的完成信号。
	retiring map[uint64]*retirement

	// nextRetireID 为每次退役分配唯一编号，保证登记与销账一一对应。
	nextRetireID uint64

	// rings 是每 server 的密钥池。为空表示该 server 走单密钥模式（沿用 spec.Headers）。
	ringMu sync.RWMutex
	rings  map[string]*downstream.KeyRing

	// MaxAttempts 覆盖默认的调用内重试预算。0 取全部可用密钥数：
	// 只要池里还有可用密钥，密钥级失败就透明换钥，调用方不感知。
	MaxAttempts int

	// OnKeyEvent 在密钥状态发生变化时被调用。
	// 用回调而不让 Pool 直接写库：连接池不该知道存储的存在，也便于分别测试。
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

	// CoolingUntil 是这次冷却的到点时刻：上层据此把冷却落库，下次启动才能恢复。
	CoolingUntil time.Time

	// CollectiveProtection 表示"本会被弃用，但因集体失败保护而未弃用"。
	// 这个信号意味着问题多半在配置或账号层面，而不是密钥本身。
	CollectiveProtection bool

	At time.Time
}

// pooled 是一条连接及其配置指纹。
type pooled struct {
	conn *downstream.Conn

	// spec 与 fingerprint 记录这条连接按哪份配置建起来。
	// 指纹用于判断配置变更要不要重建——它不含 headers，
	// 所以轮换凭据不会走重建这条路。
	spec        downstream.Spec
	fingerprint string

	// dialMu 让同一 server 的并发首次连接只发生一次，
	// 避免 16 个并发请求触发 16 次握手。
	dialMu sync.Mutex
}

// retirement 是一次连接收尾的过程：把连接与收尾开始时刻绑在一起，供管理接口
// 估算还剩多久（Deadline）、是否已超等待上限。
// id 是这次收尾的唯一身份：登记与销账都只认它，goroutine 只关自己那条记录。
type retirement struct {
	id        uint64
	serverID  string
	serverKey string
	conn      drainConn
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
		retiring: make(map[uint64]*retirement),
		rings:    make(map[string]*downstream.KeyRing),
		log:      logger,
		now:      time.Now,
	}
}

// drainTimeout 返回收尾等待上限。
func (p *Pool) drainTimeout() time.Duration {
	if p.DrainTimeout > 0 {
		return p.DrainTimeout
	}
	return DefaultDrainTimeout
}

// Drop 立即关闭并移除某条连接，只在确实要立刻停止服务时用（进程退出、连接已确证损坏）。
// 配置变更、停用、删除都该走 Retire：点一下后台开关不该把正在跑的任务一起干掉。
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

// UpdateCredentials 把新凭据交给现有连接，从下一个请求起生效。
//
// 找不到连接不算问题：下次建连会直接用最新配置，凭据按请求注入，
// 连接不重建、在飞调用不受影响。
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

// Target 是池子认识的一个下游：连接配置 + 当前是否启用。
//
// 启用与否不属于连接配置，但密钥池需要它：停用的 server 不该继续持有可用密钥。
type Target struct {
	Spec    downstream.Spec
	Enabled bool
}

// Sync 让连接池与一组最新的目标对齐：消失的连接退场、目标变更的重建。
// 只处理连接生命周期，不改动 Registry；判据是连接指纹而不是"配置变了没有"，
// 只有真换了目标（url/command/env/超时/协议版本）才重建。
// 停用 server 的连接由停用路径自己退役（这里重复会打断在飞调用），但密钥池必须在这里清掉。
func (p *Pool) Sync(targets []Target) {
	want := make(map[string]Target, len(targets))
	for _, t := range targets {
		want[t.Spec.ID] = t
	}

	p.mu.RLock()
	var gone, changed []string
	for id, entry := range p.conns {
		t, ok := want[id]
		if !ok {
			gone = append(gone, id)
			continue
		}
		if entry.fingerprint != t.Spec.ConnFingerprint() {
			changed = append(changed, id)
		}
	}
	p.mu.RUnlock()

	// 退避窗口属于"上一次那个目标"：目标换了或 server 被删了，旧窗口
	// 不该继续拦住新目标。否则改完 URL 还要等旧退避到点才能重试。
	if len(gone) > 0 || len(changed) > 0 {
		p.mu.Lock()
		for _, id := range gone {
			delete(p.fail, id)
		}
		for _, id := range changed {
			delete(p.fail, id)
		}
		p.mu.Unlock()
	}

	// 两条路径都走优雅退场：配置变更不是打断在飞调用的理由。
	for _, id := range gone {
		p.Retire(id)
	}
	for _, id := range changed {
		p.Retire(id)
	}

	// 清掉已消失或已停用的 server 的密钥池：池子里存着密钥明文。
	// 停用的密钥还在库里、重新启用时会装回来，但留在池里就可能被选中发出去。
	p.ringMu.Lock()
	for id := range p.rings {
		t, ok := want[id]
		if !ok || !t.Enabled {
			delete(p.rings, id)
		}
	}
	p.ringMu.Unlock()
}
