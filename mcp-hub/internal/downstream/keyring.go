package downstream

import (
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"sync"
	"time"
)

// CredentialInput 是装载密钥池的输入快照，由存储层折算而来。
type CredentialInput struct {
	ID       string
	Label    string
	Headers  map[string]string
	Enabled  bool
	State    KeyState // active | disabled（cooling 是内存态，不会出现在这里）
	Disabled DisabledInfo

	// CoolingUntil 是上次进程落库的冷却到点时刻（O4），零值表示没有。
	// 只在 entry 首次创建时用于恢复内存态——之后的 Sync 不再读它，
	// 否则改一次 label 就能把运行中的冷却改回去。
	CoolingUntil time.Time

	RPMLimit int
}

// DisabledInfo 描述一把密钥被弃用的原因。
//
// 随 CredentialInput 一起传入而非只留在内存：进程重启后界面仍要能说明
// "为什么它是灰的"，否则用户只能看到一个没有解释的禁用状态。
type DisabledInfo struct {
	Kind KeyVerdict
	At   time.Time
	Note string
}

// KeyRingOptions 是密钥池的行为参数。零值全部取默认。
type KeyRingOptions struct {
	ServerID  string
	ServerKey string

	RevokedThreshold int
	CoolingBase      time.Duration
	CoolingCap       time.Duration
	JitterRatio      float64

	// LimitedDisableThreshold 是连续限流多少次后弃用（R16）。
	// 0 取默认；负数显式关闭，退回"5 分钟循环永不停"的旧行为。
	LimitedDisableThreshold int

	// Now 可注入以求可测。
	Now func() time.Time
	// Rand 可注入以求可测。退避抖动用它取值。
	Rand *rand.Rand
}

// KeyRing 管理一个 server 的多把密钥。
//
// 纯粹的内存结构：持久状态（label、headers、disabled）由调用方通过 Sync 灌入，
// 判决结果通过 KeyOutcome 交回调用方落库。密钥池本身没有 I/O，因此测试里可以
// 把时间拧到任意一刻。
type KeyRing struct {
	opts KeyRingOptions

	mu      sync.Mutex
	entries []*keyEntry
	byID    map[string]*keyEntry
	cursor  int
}

// keyEntry 是池中一把密钥的全部状态：持久部分由 Sync 刷新，运行态只活在内存。
type keyEntry struct {
	id      string
	label   string
	headers map[string]string

	enabled bool

	// state 是持久状态：active | disabled。冷却不在这里，见 coolingUntil。
	state        KeyState
	disabledKind KeyVerdict
	disabledAt   time.Time
	disabledNote string

	// 运行时状态（内存，重启即消失）
	coolingUntil       time.Time
	consecutiveLimited int
	consecutiveRevoked int

	// hadCooling 标记"进过冷却且还没成功过"。冷却到期本身不算恢复——被限流的
	// 密钥在到点后第一次成功，才说明它真的回来了。
	hadCooling bool

	rpm *quotaWindow

	lastVerdict   KeyVerdict
	lastVerdictAt time.Time
	lastError     string

	picks      uint64
	lastUsedAt time.Time
}

// NewKeyRing 按输入快照构造密钥池。
func NewKeyRing(opts KeyRingOptions, creds []CredentialInput) *KeyRing {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.RevokedThreshold <= 0 {
		opts.RevokedThreshold = DefaultRevokedThreshold
	}
	if opts.CoolingBase <= 0 {
		opts.CoolingBase = DefaultCoolingBase
	}
	if opts.CoolingCap <= 0 {
		opts.CoolingCap = DefaultCoolingCap
	}
	if opts.LimitedDisableThreshold == 0 {
		opts.LimitedDisableThreshold = DefaultLimitedDisableThreshold
	}
	// 负数表示"显式关闭抖动"。用 0 表示"取默认"，是因为零值必须能构造出一个
	// 合理的池子；而测试需要确定性，得有个方式说"别抖"。
	switch {
	case opts.JitterRatio < 0:
		opts.JitterRatio = 0
	case opts.JitterRatio == 0:
		opts.JitterRatio = defaultJitterRatio
	}
	if opts.Rand == nil {
		opts.Rand = rand.New(rand.NewSource(opts.Now().UnixNano()))
	}

	r := &KeyRing{opts: opts, byID: make(map[string]*keyEntry, len(creds))}
	r.Sync(creds)
	return r
}

// Sync 让密钥池与一份最新的持久快照对齐。
//
// 已存在的密钥按 ID 复用同一个 entry，**保留冷却与计数**：改一次 label 就把
// 冷却清掉，等于给用户开了个"点一下即可绕过限流"的后门。新出现的建 entry，
// 消失的移出（连同运行态一起丢弃）。
func (r *KeyRing) Sync(creds []CredentialInput) {
	r.mu.Lock()
	defer r.mu.Unlock()

	sorted := make([]CredentialInput, len(creds))
	copy(sorted, creds)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })

	next := make([]*keyEntry, 0, len(sorted))
	nextByID := make(map[string]*keyEntry, len(sorted))

	for _, c := range sorted {
		e, ok := r.byID[c.ID]
		if !ok {
			e = newKeyEntry(c)
			// O4：恢复上一进程落库的冷却。只为新 entry 做——
			// 已存在的 entry 带着此刻真实的运行态，用库里的旧值覆盖它
			// 等于"改个备注就能把冷却改回去"。
			//
			// 过期值直接丢：库里那个时刻早就过去了，按"没在冷却"处理。
			// hadCooling 一并置位，好让冷却后的第一次成功报出 Recovered
			// ——它是"这把钥匙真的回来了"的唯一信号。
			if c.CoolingUntil.After(r.opts.Now()) {
				e.coolingUntil = c.CoolingUntil
				e.hadCooling = true
			}
		}
		e.apply(c)
		next = append(next, e)
		nextByID[c.ID] = e
	}

	r.entries = next
	r.byID = nextByID
	if r.cursor >= len(next) {
		r.cursor = 0
	}
}

// newKeyEntry 建一个 entry，配额窗口按密钥配置初始化。
func newKeyEntry(c CredentialInput) *keyEntry {
	return &keyEntry{
		id: c.ID,
		rpm: &quotaWindow{
			limit:  c.RPMLimit,
			window: time.Minute,
		},
	}
}

// apply 用最新的持久配置覆盖可持久字段，运行态（冷却、计数）一概不动。
func (e *keyEntry) apply(c CredentialInput) {
	e.label = c.Label
	e.headers = c.Headers
	e.enabled = c.Enabled

	// 持久状态以库为准：人工在别处启用过，内存里的旧判决必须让位。
	e.state = c.State
	if e.state == "" {
		e.state = KeyActive
	}
	if e.state == KeyDisabled {
		e.disabledKind = c.Disabled.Kind
		e.disabledAt = c.Disabled.At
		e.disabledNote = c.Disabled.Note
	} else {
		e.disabledKind = ""
		e.disabledNote = ""
	}

	e.rpm.setLimit(c.RPMLimit)
}

// Len 返回池中密钥总数。
func (r *KeyRing) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.entries)
}

// ErrNoUsableKey 表示池中此刻没有可用密钥。
//
// 携带 RetryAfter（最近的恢复时刻）与各状态的计数，让上游能给出"等 12 秒再试"
// 这样可执行的指引，而不是一句"暂时不可用"。
type ErrNoUsableKey struct {
	ServerKey  string
	Total      int
	Cooling    int
	Disabled   int
	Exhausted  int
	RetryAfter time.Duration

	// Cause 是导致"没密钥可用"的直接原因（通常是最后一次 429/401）。
	// 它比计数更接近用户想问的那件事，所以不能被丢掉。
	Cause error `json:"-"`
}

func (e *ErrNoUsableKey) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "server %s 没有可用密钥（共 %d 把", e.ServerKey, e.Total)
	if e.Cooling > 0 {
		fmt.Fprintf(&b, "，%d 把冷却中", e.Cooling)
	}
	if e.Exhausted > 0 {
		fmt.Fprintf(&b, "，%d 把配额用尽", e.Exhausted)
	}
	if e.Disabled > 0 {
		fmt.Fprintf(&b, "，%d 把已弃用", e.Disabled)
	}
	b.WriteString("）")
	if e.RetryAfter > 0 {
		fmt.Fprintf(&b, "，约 %s 后重试", e.RetryAfter.Round(time.Second))
		// 混合场景：冷却的钥到点会自己回来，但弃用的不会。只说"重试"
		// 会让人（和 AI）以为等着就行，而其中一部分永远等不来。
		if e.Disabled > 0 {
			fmt.Fprintf(&b, "；已弃用的 %d 把不会自动恢复，需人工处理", e.Disabled)
		}
	} else if e.Disabled > 0 {
		// 纯弃用场景没有任何"到点恢复"可言：重试无意义，话术必须直说人工。
		fmt.Fprintf(&b, "，需要人工处理：弃用的密钥不会自动恢复，请到管理界面处理")
	}
	if e.Cause != nil {
		fmt.Fprintf(&b, "；最近一次失败：%v", e.Cause)
	}
	return b.String()
}

// Unwrap 让 errors.Is/As 能穿透到直接原因。
func (e *ErrNoUsableKey) Unwrap() error { return e.Cause }

// KeyLease 是一次密钥借用。
//
// label 与 headers 是借用时刻的配置快照：Acquire 返回后调用方不再持有 KeyRing
// 的锁，而管理端操作（改密钥头、停用、重载）随时会在锁内改写同一 entry。若在
// 这里直接读 entry 就是数据竞争。快照在持锁期间摘取，本次调用因此与后续改动
// 隔离——"这次用借用那一刻的凭据"，下次借用自然拿到新值。
type KeyLease struct {
	ring *KeyRing
	key  *keyEntry

	// label/headers：借用时刻的快照。
	label   string
	headers map[string]string
}

// ID 返回密钥 id。id 在 entry 创建后不变，可直接读。
func (l *KeyLease) ID() string {
	if l == nil || l.key == nil {
		return ""
	}
	return l.key.id
}

// Label 返回密钥展示名。
func (l *KeyLease) Label() string {
	if l == nil {
		return ""
	}
	return l.label
}

// Headers 返回这次调用要注入的完整头。
func (l *KeyLease) Headers() map[string]string {
	if l == nil {
		return nil
	}
	return l.headers
}

// Acquire 借出一把可用密钥。
//
// 选法是"原子游标 + 顺时针找第一个可用者"，即轮询。不用"最少在飞"：轮询是为了
// 摊薄流量、避免某把被打到限流，而"谁快用谁"反而会让流量集中。
//
// exclude 是本次调用内已失败过的密钥，重试必须排除——再打一次只会加重限流。
func (r *KeyRing) Acquire(exclude map[string]bool) (*KeyLease, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if len(r.entries) == 0 {
		return nil, &ErrNoUsableKey{ServerKey: r.opts.ServerKey, Total: 0}
	}

	lease, noKey := r.pickLocked(r.opts.Now(), exclude, true)
	if noKey != nil {
		return nil, noKey
	}
	return lease, nil
}

// Unavailable 报告"此刻是否真的一把也借不到"，但不消耗任何配额。
//
// 一次调用里换了几把密钥都失败后，上层需要判断该报"下游在限流"还是"密钥池空了"。
// 后者携带 retryAfter 与各状态计数，对调用方（以及背后的 AI）是可执行的；
// 前者只是一句现象描述。它只回答问题，不改变状态。
func (r *KeyRing) Unavailable(exclude map[string]bool) *ErrNoUsableKey {
	r.mu.Lock()
	defer r.mu.Unlock()

	if len(r.entries) == 0 {
		return &ErrNoUsableKey{ServerKey: r.opts.ServerKey, Total: 0}
	}

	_, noKey := r.pickLocked(r.opts.Now(), exclude, false)
	return noKey
}

// pickLocked 是选密钥的唯一实现：从游标顺时针找第一个可用者。
//
// commit 为 false 时只做判断，不预扣配额、不推进游标、不记 picks。两条路径
// 共用同一份状态统计，否则"为什么借不到"的解释迟早会与实际选法不一致。
func (r *KeyRing) pickLocked(now time.Time, exclude map[string]bool, commit bool) (*KeyLease, *ErrNoUsableKey) {
	n := len(r.entries)

	var (
		cooling, disabled, exhausted int
		soonest                      time.Duration
		hasSoonest                   bool
	)
	note := func(wait time.Duration) {
		if wait > 0 && (!hasSoonest || wait < soonest) {
			soonest, hasSoonest = wait, true
		}
	}

	for i := 0; i < n; i++ {
		idx := (r.cursor + i) % n
		e := r.entries[idx]
		if exclude[e.id] {
			continue
		}

		if !e.usable(now) {
			if e.enabled && e.state != KeyDisabled && e.inCooldown(now) {
				cooling++
				note(e.coolingUntil.Sub(now))
			} else {
				disabled++
			}
			continue
		}

		if ok, wait := e.rpm.allow(now, 1); !ok {
			exhausted++
			note(wait)
			continue
		}

		if !commit {
			return &KeyLease{ring: r, key: e, label: e.label, headers: e.headers}, nil
		}

		e.rpm.precharge(now, 1)
		e.picks++
		e.lastUsedAt = now

		r.cursor = (idx + 1) % n
		return &KeyLease{ring: r, key: e, label: e.label, headers: e.headers}, nil
	}

	err := &ErrNoUsableKey{
		ServerKey: r.opts.ServerKey,
		Total:     n,
		Cooling:   cooling,
		Disabled:  disabled,
		Exhausted: exhausted,
	}
	if hasSoonest {
		err.RetryAfter = soonest
	}
	return nil, err
}
