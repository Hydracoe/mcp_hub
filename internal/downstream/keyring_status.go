package downstream

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"time"
)

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

	// ConsecutiveLimited 是连续限流次数，退避进度靠它展示；它不会因为冷却到期
	// 而重置，只有一次成功才清零。
	ConsecutiveLimited int `json:"consecutiveLimited,omitempty"`

	WindowEnd time.Time `json:"windowEnd,omitempty"`

	RPMUsed  int `json:"rpmUsed"`
	RPMLimit int `json:"rpmLimit"`

	Picks      uint64    `json:"picks"`
	LastUsedAt time.Time `json:"lastUsedAt,omitempty"`
	LastError  string    `json:"lastError,omitempty"`

	// Fingerprint 是密钥内容的短指纹，让用户分得清"是哪一把"。它不是用来验证
	// 密钥对不对的——那需要把明文交出去。
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
// 只清计数，不动冷却：本地计数是网关的保守估计，用户比网关更清楚真实配额；
// 而冷却是下游给的客观信号，清掉就等于骗自己。
func (r *KeyRing) ResetQuota(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	e, ok := r.byID[id]
	if !ok {
		return false
	}
	e.rpm.reset()
	return true
}

// ClearCooling 解除某把密钥的冷却（人工干预）。
//
// 冷却与"进过冷却"的标记一起复位：人工把冷却清掉之后，密钥是被人放回池子的，
// 下一次成功不是"冷却后的首次成功"，不该报出 Recovered——那个信号说的是
// "它自己真的回来了"，人工解除的冷却谈不上回来。
//
// 只复位冷却相关的标记，不动连续限流计数：M8 明确"人工启用不清计数"，
// "再犯立刻弃用"是威慑，清了就没了。
func (r *KeyRing) ClearCooling(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	e, ok := r.byID[id]
	if !ok {
		return false
	}
	e.coolingUntil = time.Time{}
	e.hadCooling = false
	return true
}

// ResetLimitedCount 清零某把密钥的连续限流计数与退避标记（O1 自动救回专用）。
//
// 与人工启用的语义刻意不同（M8：人工启用不清计数，"再犯立刻弃用"是明确
// 的威慑）：自动救回的前提是探活已通过，带着顶格的旧计数回池，下一次 429
// 会立刻把它再禁掉，形成禁用↔救回的抖动。探活通过即视为干净的起点。
func (r *KeyRing) ResetLimitedCount(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	e, ok := r.byID[id]
	if !ok {
		return false
	}
	e.consecutiveLimited = 0
	e.hadCooling = false
	return true
}

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
// 取全部头值拼接后的哈希前 4 位。不取末尾字符：有些供应商的密钥末尾是固定
// 后缀，那样每把看起来都一样。
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

// usable 报告这把密钥此刻能否被选中。
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

// status 组装该密钥的对外快照。
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
	return out
}
