package downstream

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/rand"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// KeyState 是一把密钥的运行状态。
//
// 只有三种，且 active/cooling/disabled 的恢复方式各不相同：
// cooling 到点自动恢复，disabled 只有人工能救。把两者合成一个
// "不可用"是这类系统最常见的语义丢失——用户会以为等着就好。
type KeyState string

const (
	KeyActive   KeyState = "active"
	KeyCooling  KeyState = "cooling"
	KeyDisabled KeyState = "disabled"
)

// KeyVerdict 是针对密钥调度作出的判决。
//
// 与 ErrorKind 并列而非合并：ErrorKind 回答"下游为什么失败"，面向人；
// KeyVerdict 回答"这把密钥还能不能用"，面向调度器。同一次观测可以
// 给出两个不同结论——5xx 对前者是 UPSTREAM_ERROR，对后者是"与密钥
// 无关，别动它"。合并成一个枚举，任何一侧加值都会污染另一侧。
type KeyVerdict string

const (
	// VerdictOK 表示调用成功。它不是"中性失败"，而是唯一能重置
	// 连续限流计数的信号——冷却到期却不重置，退避就永远升不上去。
	VerdictOK KeyVerdict = "OK"

	// VerdictNeutral 表示失败与密钥无关（5xx、超时、网络、业务错误）。
	VerdictNeutral KeyVerdict = "NEUTRAL"

	VerdictRateLimited    KeyVerdict = "RATE_LIMITED"
	VerdictQuotaExhausted KeyVerdict = "QUOTA_EXHAUSTED"
	VerdictInvalidKey     KeyVerdict = "INVALID_KEY"
	VerdictKeyRevoked     KeyVerdict = "KEY_REVOKED"
)

// VerdictOf 由错误分类与观测推导密钥判决。
//
// 401 与 403 在这里分道扬镳：401 语义无歧义（凭据不对），403 不是——
// 它也可能是 IP 白名单、scope 不足、地区封锁，换密钥同样没用。
// 因此 403 给两次机会，代价只是多打一次请求。
func VerdictOf(kind ErrorKind, obs Observation) KeyVerdict {
	switch kind {
	case KindRateLimited:
		return VerdictRateLimited
	case KindQuotaExhausted:
		return VerdictQuotaExhausted
	case KindAuthRequired:
		if obs.StatusCode == http.StatusUnauthorized {
			return VerdictInvalidKey
		}
		return VerdictKeyRevoked
	case KindNone:
		return VerdictOK
	}
	return VerdictNeutral
}

// Retryable 报告该判决是否允许"换一把密钥再试一次"。
//
// 只有确定未被执行的四类信号在此列：401 / 402 / 403 / 429。
// 超时、5xx 与业务错误都可能**已经执行过**——密钥池让重试变便宜了，
// 但"可能重复下单"没有变便宜，对有副作用的工具这是两码事。
func (v KeyVerdict) Retryable() bool {
	switch v {
	case VerdictRateLimited, VerdictQuotaExhausted, VerdictInvalidKey, VerdictKeyRevoked:
		return true
	}
	return false
}

// Disables 报告该判决是否指向"最终弃用"。
func (v KeyVerdict) Disables() bool {
	switch v {
	case VerdictQuotaExhausted, VerdictInvalidKey, VerdictKeyRevoked:
		return true
	}
	return false
}

// Human 返回面向用户的中文解释。
func (v KeyVerdict) Human() string {
	switch v {
	case VerdictOK:
		return "调用成功"
	case VerdictNeutral:
		return "失败与密钥无关"
	case VerdictRateLimited:
		return "被限流：冷却后自动恢复"
	case VerdictQuotaExhausted:
		return "额度或余额耗尽：需要更换或充值"
	case VerdictInvalidKey:
		return "凭据无效：已弃用"
	case VerdictKeyRevoked:
		return "凭据被吊销或封禁"
	}
	return string(v)
}

// 退避与判定的默认参数。
const (
	// DefaultCoolingBase 是指数退避的基数：1s、2s、4s……
	DefaultCoolingBase = time.Second

	// DefaultCoolingCap 是冷却上限 5 分钟。
	//
	// RPM / TPM 的窗口本就是分钟级，退避到这个量级再往上涨没有意义；
	// 真正需要等更久的，应该归类为 QUOTA_EXHAUSTED 走弃用——
	// "等 5 分钟"和"这把没钱了"是两种不同的处境。
	DefaultCoolingCap = 5 * time.Minute

	// DefaultRevokedThreshold 是 403 连续多少次后弃用。
	DefaultRevokedThreshold = 2

	// collectiveWindow 是判断"集体失败"的时间窗。窗口内多把密钥
	// 因同一原因失败，才认为是配置或账号层面的问题。
	collectiveWindow = time.Minute

	// defaultJitterRatio 是退避抖动幅度：避免多把密钥同时到点、
	// 同时冲击下游。
	defaultJitterRatio = 0.2
)

// CredentialInput 是装载密钥池的输入快照，由存储层折算而来。
type CredentialInput struct {
	ID       string
	Label    string
	Headers  map[string]string
	Enabled  bool
	State    KeyState // active | disabled（cooling 是内存态，不会出现在这里）
	Disabled DisabledInfo

	RPMLimit int
	TPMLimit int
}

// DisabledInfo 描述一把密钥被弃用的原因。
//
// 随 CredentialInput 一起传入而非只留在内存：进程重启后界面仍要
// 能说明"为什么它是灰的"，否则用户只能看到一个没有解释的禁用状态。
type DisabledInfo struct {
	Kind KeyVerdict
	At   time.Time
	Note string
}

// KeyRingOptions 是密钥池的行为参数。零值全部取默认。
type KeyRingOptions struct {
	ServerID  string
	ServerKey string

	// EstTokensPerCall 是 TPM 的中间回退值，0 表示不计量。
	EstTokensPerCall int

	RevokedThreshold int
	CoolingBase      time.Duration
	CoolingCap       time.Duration
	JitterRatio      float64

	// Now 可注入以求可测。
	Now func() time.Time
	// Rand 可注入以求可测。退避抖动用它取值。
	Rand *rand.Rand
}

// KeyRing 管理一个 server 的多把密钥。
//
// 它是纯粹的内存结构：持久状态（label、headers、disabled）由调用方
// 通过 Sync 灌入，判决结果通过 KeyOutcome 交回调用方落库。这样
// 密钥池本身没有 I/O，可以在测试里把时间拧到任意一刻。
type KeyRing struct {
	opts KeyRingOptions

	mu      sync.Mutex
	entries []*keyEntry
	byID    map[string]*keyEntry
	cursor  int
}

type keyEntry struct {
	id      string
	label   string
	headers map[string]string

	enabled bool

	// state 是持久状态：active | disabled。冷却不在这里——
	// 它是内存态，见 coolingUntil。
	state        KeyState
	disabledKind KeyVerdict
	disabledAt   time.Time
	disabledNote string

	// 运行时状态（内存，重启即消失）
	coolingUntil       time.Time
	consecutiveLimited int
	consecutiveRevoked int

	// hadCooling 标记"进过冷却且还没成功过"。
	// 冷却到期本身不算恢复——被限流的密钥在到点后第一次成功，
	// 才说明它真的回来了。
	hadCooling bool

	rpm *quotaWindow
	tpm *quotaWindow

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
	// 负数表示"显式关闭抖动"。用 0 表示"取默认"是因为零值必须能
	// 构造出一个合理的池子；而测试需要确定性，得有个方式说"别抖"。
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
// 已存在的密钥按 ID 复用同一个 entry，**保留冷却与计数**：改一次
// label 就把冷却清掉，等于给用户开了个"点一下即可绕过限流"的后门。
// 新出现的建 entry，消失的移出（连同它的运行态一起丢弃）。
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
			e = &keyEntry{
				id: c.ID,
				rpm: &quotaWindow{
					limit:  c.RPMLimit,
					window: time.Minute,
					policy: windowReject,
				},
				tpm: &quotaWindow{
					limit:  c.TPMLimit,
					window: time.Minute,
					policy: windowIgnoreWhenUnmetered,
				},
			}
		}
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
		e.tpm.setLimit(c.TPMLimit)

		next = append(next, e)
		nextByID[c.ID] = e
	}

	r.entries = next
	r.byID = nextByID
	if r.cursor >= len(next) {
		r.cursor = 0
	}
}

// Len 返回池中密钥总数。
func (r *KeyRing) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.entries)
}

// ErrNoUsableKey 表示池中此刻没有可用密钥。
//
// 携带 RetryAfter（最近的恢复时刻）与各状态的计数，让上游能给出
// "等 12 秒再试"这样可执行的指引，而不是一句"暂时不可用"。
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
	}
	if e.Cause != nil {
		fmt.Fprintf(&b, "；最近一次失败：%v", e.Cause)
	}
	return b.String()
}

// Unwrap 让 errors.Is/As 能穿透到直接原因。
func (e *ErrNoUsableKey) Unwrap() error { return e.Cause }

// KeyLease 是一次密钥借用。
type KeyLease struct {
	ring *KeyRing
	key  *keyEntry

	// tokensEst 是本次准入按 TPM 预扣的 token 数，0 表示未计量。
	tokensEst int
}

// ID 返回密钥 id。
func (l *KeyLease) ID() string {
	if l == nil || l.key == nil {
		return ""
	}
	return l.key.id
}

// Label 返回密钥展示名。
func (l *KeyLease) Label() string {
	if l == nil || l.key == nil {
		return ""
	}
	return l.key.label
}

// Headers 返回这次调用要注入的完整头。
func (l *KeyLease) Headers() map[string]string {
	if l == nil || l.key == nil {
		return nil
	}
	return l.key.headers
}

// TokensEstimate 返回本次预扣的 token 数。
func (l *KeyLease) TokensEstimate() int {
	if l == nil {
		return 0
	}
	return l.tokensEst
}

// Acquire 借出一把可用密钥。
//
// 选法是"原子游标 + 从游标处顺时针找第一个可用者"，即轮询。
// 不用"最少在飞"：轮询的目的是**摊薄**，让每把都均匀用上，避免某把
// 被打到限流甚至封禁；而"最少在飞"会退化成"谁快用谁"，反而集中。
//
// exclude 是本次调用内已经失败过的密钥，重试时必须排除——
// 同一把再打一次毫无意义，还会加重它的限流。
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
// 存在的理由：一次调用里换了几把密钥都失败后，上层需要判断该报
// "下游在限流"还是"密钥池空了"。后者携带 retryAfter 与各状态计数，
// 对调用方（以及背后的 AI）是可执行的；前者只是一句现象描述。
//
// 探测不预扣配额、不推进游标——它只回答问题，不改变状态。
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
// commit 为 false 时只做判断，不预扣配额、不推进游标、不记 picks。
// 两条路径共用同一份状态统计，否则"为什么借不到"的解释迟早会与
// 实际的选法不一致。
func (r *KeyRing) pickLocked(now time.Time, exclude map[string]bool, commit bool) (*KeyLease, *ErrNoUsableKey) {
	n := len(r.entries)

	var (
		cooling, disabled, exhausted int
		soonest                      time.Duration
		hasSoonest                   bool
	)

	for i := 0; i < n; i++ {
		idx := (r.cursor + i) % n
		e := r.entries[idx]
		if exclude[e.id] {
			continue
		}

		if !e.usable(now) {
			switch {
			case e.enabled && e.state != KeyDisabled && e.inCooldown(now):
				cooling++
				if d := e.coolingUntil.Sub(now); d > 0 && (!hasSoonest || d < soonest) {
					soonest, hasSoonest = d, true
				}
			default:
				disabled++
			}
			continue
		}

		if ok, wait := e.rpm.allow(now, 1); !ok {
			exhausted++
			if wait > 0 && (!hasSoonest || wait < soonest) {
				soonest, hasSoonest = wait, true
			}
			continue
		}

		est := r.opts.EstTokensPerCall
		if ok, wait := e.tpm.allow(now, est); !ok {
			exhausted++
			if wait > 0 && (!hasSoonest || wait < soonest) {
				soonest, hasSoonest = wait, true
			}
			continue
		}

		if !commit {
			return &KeyLease{ring: r, key: e, tokensEst: est}, nil
		}

		e.rpm.precharge(now, 1)
		e.tpm.precharge(now, est)
		e.picks++
		e.lastUsedAt = now

		r.cursor = (idx + 1) % n
		return &KeyLease{ring: r, key: e, tokensEst: est}, nil
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

// LeaseResult 是一次调用的反馈。
type LeaseResult struct {
	Verdict KeyVerdict
	Obs     Observation
	// ActualTokens 是下游声明的 token 用量，负数表示未声明。
	ActualTokens int
}

// KeyOutcome 是反馈之后密钥的状态变化，供调用方落库与记日志。
type KeyOutcome struct {
	Verdict  KeyVerdict
	State    KeyState
	KeyLabel string

	// Cooling 非零表示本次把密钥打入冷却。
	Cooling      time.Duration
	CoolingUntil time.Time

	// Disabled 表示本次把密钥弃用（需要落库）。
	Disabled bool

	// Recovered 表示这把密钥冷却之后第一次成功，即真的恢复了。
	// 与"冷却到期"是两件事：到期只说明回到了候选队列，
	// 成功才说明下游不再限它。
	Recovered bool

	// CollectiveProtection 表示"本会被弃用，但因集体失败保护而未弃用"。
	// 这个信号很要紧：它意味着问题多半不在密钥本身，而在配置或账号层面。
	CollectiveProtection bool

	// ConsecutiveLimited 是累计连续限流次数，用于展示退避进度。
	ConsecutiveLimited int

	Detail string
}

// Done 反馈一次调用的结果，并据此推进密钥状态。
func (l *KeyLease) Done(res LeaseResult) KeyOutcome {
	if l == nil || l.key == nil {
		// 单密钥模式没有租约，调用路径不该因为多了一层判断而分叉。
		return KeyOutcome{Verdict: res.Verdict}
	}
	return l.ring.settle(l, res)
}

func (r *KeyRing) settle(l *KeyLease, res LeaseResult) KeyOutcome {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := r.opts.Now()
	e := l.key

	out := KeyOutcome{
		Verdict:  res.Verdict,
		KeyLabel: e.label,
		State:    e.state,
	}

	// TPM 结算：下游声明了用量就按实际扣，否则保持预扣值。
	e.tpm.settle(now, l.tokensEst, res.ActualTokens)

	if res.Obs.Err != nil {
		e.lastError = res.Obs.Err.Error()
	} else if res.Obs.StatusCode >= http.StatusBadRequest {
		e.lastError = fmt.Sprintf("HTTP %d", res.Obs.StatusCode)
	}
	e.lastVerdict = res.Verdict
	e.lastVerdictAt = now

	switch res.Verdict {
	case VerdictOK:
		// 唯一能重置退避计数的信号。冷却到期**不**重置：
		// 否则会退化成 1s → 重试 → 429 → 1s 的死循环。
		if e.hadCooling {
			out.Recovered = true
			out.Detail = fmt.Sprintf("冷却后首次成功，恢复可用（此前连续限流 %d 次）",
				e.consecutiveLimited)
			e.hadCooling = false
			e.coolingUntil = time.Time{}
		}
		e.consecutiveLimited = 0
		e.consecutiveRevoked = 0
		e.lastError = ""

	case VerdictRateLimited:
		e.consecutiveLimited++
		wait := r.coolingFor(e, res.Obs.RetryAfter)
		e.coolingUntil = now.Add(wait)
		e.state = KeyActive // 冷却不改持久状态
		e.hadCooling = true

		out.Cooling = wait
		out.CoolingUntil = e.coolingUntil
		out.State = KeyCooling
		out.ConsecutiveLimited = e.consecutiveLimited
		if res.Obs.RetryAfter > 0 {
			out.Detail = fmt.Sprintf("下游要求等待 %s（Retry-After）",
				res.Obs.RetryAfter.Round(time.Second))
		} else {
			out.Detail = fmt.Sprintf("连续限流第 %d 次，退避 %s",
				e.consecutiveLimited, wait.Round(time.Second))
		}

	case VerdictQuotaExhausted, VerdictInvalidKey:
		r.disable(e, res.Verdict, &out, now,
			fmt.Sprintf("%s，该密钥不再参与轮换", res.Verdict.Human()))

	case VerdictKeyRevoked:
		e.consecutiveRevoked++
		if e.consecutiveRevoked < r.opts.RevokedThreshold {
			out.State = e.state
			out.Detail = fmt.Sprintf(
				"403 第 %d 次；连续 %d 次才弃用（403 也可能来自 IP 白名单或权限范围，换密钥未必有用）",
				e.consecutiveRevoked, r.opts.RevokedThreshold)
			break
		}
		r.disable(e, res.Verdict, &out, now,
			fmt.Sprintf("连续 %d 次 403，判定该密钥不可用", e.consecutiveRevoked))

	default: // VerdictNeutral
		// 与密钥无关。刻意不动任何计数：5xx 变多不代表限流结束，
		// 也不该把已经累计的退避进度清掉。
		out.State = e.state
	}

	if out.State == "" {
		out.State = e.state
	}
	return out
}

// disable 执行弃用，但先过集体失败保护。
func (r *KeyRing) disable(e *keyEntry, v KeyVerdict, out *KeyOutcome, now time.Time, detail string) {
	out.Detail = detail

	// 集体失败保护：禁用这把会让可用密钥归零，而窗口内已有别的密钥
	// 因同一原因失败 —— 那多半不是密钥的问题，是配置或账号的问题。
	//
	// 没有这道闸，网关会尽职尽责地把用户填的 20 把密钥逐个禁用，
	// 用户回来只看到一片红，且不知道第一把是从哪坏起的。
	if r.wouldWipeOut(e, v, now) {
		out.CollectiveProtection = true
		out.Disabled = false
		out.State = e.state
		out.Detail = fmt.Sprintf(
			"本应弃用该密钥（%s），但弃用后将无可用密钥，且窗口内其他密钥因同一原因失败——"+
				"判定为配置或账号层面的问题，保留该密钥并把 server 标为降级", v.Human())
		return
	}

	e.state = KeyDisabled
	e.disabledKind = v
	e.disabledAt = now
	e.disabledNote = detail
	out.Disabled = true
	out.State = KeyDisabled
}

// wouldWipeOut 判断弃用 e 是否会导致可用密钥归零，且存在同因失败的其他密钥。
//
// 门槛刻意做得简单（会归零 + 窗口内 + 同分类）：它必须在禁用**之前**
// 算出来，复杂规则在这里只会变成又一个出错点。
func (r *KeyRing) wouldWipeOut(e *keyEntry, v KeyVerdict, now time.Time) bool {
	othersUsable := 0
	sameVerdict := 0

	for _, x := range r.entries {
		if x == e {
			continue
		}
		if x.usable(now) {
			othersUsable++
		}
		if x.lastVerdict == v && !x.lastVerdictAt.IsZero() &&
			now.Sub(x.lastVerdictAt) <= collectiveWindow {
			sameVerdict++
		}
	}

	return othersUsable == 0 && sameVerdict > 0
}

// coolingFor 计算冷却时长：Retry-After 优先，否则指数退避加抖动。
func (r *KeyRing) coolingFor(e *keyEntry, retryAfter time.Duration) time.Duration {
	if retryAfter > 0 {
		return retryAfter
	}

	shift := min(e.consecutiveLimited-1, 20)
	wait := r.opts.CoolingBase << shift
	if wait > r.opts.CoolingCap || wait <= 0 {
		wait = r.opts.CoolingCap
	}
	return jitter(wait, r.opts.JitterRatio, r.opts.Rand)
}

// jitter 给时长加上 ±ratio 的随机偏移。
//
// 目的是避免多把密钥在同一毫秒到点、同时冲击下游——那会把
// "分摊流量"变成"制造一次突发"。
func jitter(d time.Duration, ratio float64, rnd *rand.Rand) time.Duration {
	if ratio <= 0 || rnd == nil {
		return d
	}
	delta := (rnd.Float64()*2 - 1) * ratio
	out := time.Duration(float64(d) * (1 + delta))
	if out <= 0 {
		return d
	}
	return out
}

// KeyStatus 是一把密钥的对外快照，供管理接口展示。
type KeyStatus struct {
	ID      string   `json:"id"`
	Label   string   `json:"label"`
	State   KeyState `json:"state"`
	Enabled bool     `json:"enabled"`

	DisabledKind KeyVerdict `json:"disabledKind,omitempty"`
	DisabledAt   time.Time  `json:"disabledAt,omitempty"`
	DisabledNote string     `json:"disabledNote,omitempty"`

	// CoolingUntil 与 CoolingFor 只在 state == cooling 时有意义。
	CoolingUntil time.Time     `json:"coolingUntil,omitempty"`
	CoolingFor   time.Duration `json:"coolingFor,omitempty"`

	// ConsecutiveLimited 是连续限流次数，退避进度靠它展示；
	// 它不会因为冷却到期而重置，只有一次成功才清零。
	ConsecutiveLimited int `json:"consecutiveLimited,omitempty"`

	LockedUntil time.Time `json:"lockedUntil,omitempty"`
	WindowEnd   time.Time `json:"windowEnd,omitempty"`

	RPMUsed  int `json:"rpmUsed"`
	RPMLimit int `json:"rpmLimit"`
	TPMUsed  int `json:"tpmUsed"`
	TPMLimit int `json:"tpmLimit"`
	// TPMMetered 为 false 时，界面必须显示"未计量"而不是 "0 / 100k"——
	// 一个假的 0 会让人以为一切正常。
	TPMMetered bool `json:"tpmMetered"`

	Picks      uint64    `json:"picks"`
	LastUsedAt time.Time `json:"lastUsedAt,omitempty"`
	LastError  string    `json:"lastError,omitempty"`

	// Fingerprint 是密钥内容的末 4 位指纹，用于让用户区分"是哪一把"。
	// 它不是用来验证密钥对不对的——那需要把明文交出去。
	Fingerprint string `json:"fingerprint"`
}

// Status 返回全部密钥的运行时状态。
func (r *KeyRing) Status() []KeyStatus {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := r.opts.Now()
	out := make([]KeyStatus, 0, len(r.entries))
	for _, e := range r.entries {
		out = append(out, e.status(now))
	}
	return out
}

// StatusOf 返回单把密钥的状态，不存在时返回 false。
func (r *KeyRing) StatusOf(id string) (KeyStatus, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	e, ok := r.byID[id]
	if !ok {
		return KeyStatus{}, false
	}
	return e.status(r.opts.Now()), true
}

// ResetQuota 清零某把密钥的本地配额计数。
//
// 只清计数，不动冷却：本地计数是网关的保守估计，用户比网关更清楚
// 真实配额；而冷却是下游给的客观信号，清掉就等于骗自己。
func (r *KeyRing) ResetQuota(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	e, ok := r.byID[id]
	if !ok {
		return false
	}
	e.rpm.reset()
	e.tpm.reset()
	return true
}

// ClearCooling 解除某把密钥的冷却（人工干预）。
func (r *KeyRing) ClearCooling(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	e, ok := r.byID[id]
	if !ok {
		return false
	}
	e.coolingUntil = time.Time{}
	return true
}

// EstTokensPerCall 返回当前配置的每调用预估 token 数。
func (r *KeyRing) EstTokensPerCall() int { return r.opts.EstTokensPerCall }

// UsableCount 返回此刻可参与轮换的密钥数。
func (r *KeyRing) UsableCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := r.opts.Now()
	n := 0
	for _, e := range r.entries {
		if e.usable(now) {
			n++
		}
	}
	return n
}

// KeyFingerprint 返回一组头的短指纹，用于界面上区分"是哪一把密钥"。
//
// 取全部头值拼接后的哈希前 4 位。不取末尾字符：有些供应商的密钥
// 末尾是固定后缀，那样每把看起来都一样。
func KeyFingerprint(headers map[string]string) string {
	if len(headers) == 0 {
		return ""
	}
	keys := make([]string, 0, len(headers))
	for k := range headers {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	h := sha256.New()
	for _, k := range keys {
		h.Write([]byte(k))
		h.Write([]byte{0})
		h.Write([]byte(headers[k]))
		h.Write([]byte{0})
	}
	return "…" + hex.EncodeToString(h.Sum(nil))[:4]
}

// --- entry 级判定 ---

func (e *keyEntry) usable(now time.Time) bool {
	return e.enabled && e.state != KeyDisabled && !e.inCooldown(now)
}

func (e *keyEntry) inCooldown(now time.Time) bool {
	return !e.coolingUntil.IsZero() && now.Before(e.coolingUntil)
}

// effectiveState 返回叠加了冷却的运行状态。
func (e *keyEntry) effectiveState(now time.Time) KeyState {
	switch {
	case !e.enabled, e.state == KeyDisabled:
		return KeyDisabled
	case e.inCooldown(now):
		return KeyCooling
	}
	return KeyActive
}

// retryIn 返回该密钥最早可能恢复的等待时长。
func (e *keyEntry) retryIn(now time.Time) time.Duration {
	if e.state == KeyDisabled || !e.enabled {
		return 0 // 只有人工能救，给不出时间
	}
	if d := e.coolingUntil.Sub(now); d > 0 {
		return d
	}
	if d := e.rpm.nextRefill(now); d > 0 {
		return d
	}
	return e.tpm.nextRefill(now)
}

func (e *keyEntry) status(now time.Time) KeyStatus {
	state := e.effectiveState(now)

	out := KeyStatus{
		ID:                 e.id,
		Label:              e.label,
		State:              state,
		Enabled:            e.enabled,
		DisabledKind:       e.disabledKind,
		DisabledAt:         e.disabledAt,
		DisabledNote:       e.disabledNote,
		ConsecutiveLimited: e.consecutiveLimited,
		Picks:              e.picks,
		LastUsedAt:         e.lastUsedAt,
		LastError:          e.lastError,
		Fingerprint:        KeyFingerprint(e.headers),
	}

	if state == KeyCooling {
		out.CoolingUntil = e.coolingUntil
		out.CoolingFor = e.coolingUntil.Sub(now)
	}
	if e.rpm.limit > 0 {
		out.RPMUsed, out.RPMLimit = e.rpm.snapshot(now)
		out.WindowEnd = e.rpm.windowEnd(now)
	}
	if e.tpm.limit > 0 {
		out.TPMUsed, out.TPMLimit = e.tpm.snapshot(now)
		out.TPMMetered = true
	}
	return out
}
