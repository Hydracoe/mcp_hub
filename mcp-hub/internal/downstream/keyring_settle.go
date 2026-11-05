package downstream

import (
	"fmt"
	"math/rand"
	"net/http"
	"time"
)

// LeaseResult 是一次调用的反馈。
type LeaseResult struct {
	Verdict KeyVerdict
	Obs     Observation
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

	// Recovered 表示这把密钥冷却之后第一次成功，即真的恢复了。与"冷却到期"
	// 是两件事：到期只说明回到候选队列，成功才说明下游不再限它。
	Recovered bool

	// CollectiveProtection 表示"本会被弃用，但因集体失败保护而未弃用"。
	// 它意味着问题多半不在密钥本身，而在配置或账号层面。
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

// settle 按判决更新密钥状态，并返回本次变化。
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

	if res.Obs.Err != nil {
		e.lastError = res.Obs.Err.Error()
	} else if res.Obs.StatusCode >= http.StatusBadRequest {
		e.lastError = fmt.Sprintf("HTTP %d", res.Obs.StatusCode)
	}
	e.lastVerdict = res.Verdict
	e.lastVerdictAt = now

	switch res.Verdict {
	case VerdictOK:
		// 唯一能重置退避计数的信号。冷却到期不重置：
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
		// 冷却只落在 coolingUntil，不动 e.state。调用在飞期间密钥可能已被人工
		// 停用（Sync 会在锁内写入 disabled），429 的反馈不能把那个停用抹掉。
		e.consecutiveLimited++
		wait := r.coolingFor(e, res.Obs.RetryAfter)
		e.coolingUntil = now.Add(wait)
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

		// 连续限流顶格不再无限循环。退避已到上限仍连续被拒，说明"晾一会儿能自己好"
		// 不成立，大概率是账号级限流：弃用并通知人工。集体失败保护依然生效（整池
		// 同时顶格是配置或账号问题），已弃用的不覆盖原有弃用原因。
		if t := r.opts.LimitedDisableThreshold; t > 0 &&
			e.consecutiveLimited >= t && e.state != KeyDisabled {
			r.disable(e, res.Verdict, &out, now,
				fmt.Sprintf("连续限流 %d 次，退避已顶格，疑似账号级限流，请人工确认",
					e.consecutiveLimited))
			out.Cooling = 0
			out.CoolingUntil = time.Time{}
			e.coolingUntil = time.Time{}
			out.State = e.state
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

	// 集体失败保护：禁用这把会让可用密钥归零，而窗口内已有别的密钥因同一原因
	// 失败——那多半不是密钥的问题，是配置或账号的问题。没有这道闸，网关会把
	// 用户填的密钥逐个禁用，用户只看到一片红，也不知道第一把是从哪坏起的。
	if r.wouldWipeOut(e, v, now, r.collectiveWindowFor(v)) {
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

// collectiveWindowFor 返回该判决适用的"同因失败"时间窗。
// 默认 1 分钟：401/402/403 是即时判决，同因失败会接连发生。
// 限流例外：顶格禁用的密钥大部分时间在冷却里（最长 5 分钟），窗口放宽到两个冷却上限。
func (r *KeyRing) collectiveWindowFor(v KeyVerdict) time.Duration {
	if v == VerdictRateLimited {
		return 2 * r.opts.CoolingCap
	}
	return collectiveWindow
}

// wouldWipeOut 判断弃用 e 是否会导致可用密钥归零，且存在同因失败的其他密钥。
// 门槛刻意简单（会归零 + 窗口内 + 同分类）：它必须在禁用之前算出来，
// 复杂规则在这里只会变成又一个出错点。
func (r *KeyRing) wouldWipeOut(e *keyEntry, v KeyVerdict, now time.Time, window time.Duration) bool {
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
			now.Sub(x.lastVerdictAt) <= window {
			sameVerdict++
		}
	}

	return othersUsable == 0 && sameVerdict > 0
}

// coolingFor 计算冷却时长：Retry-After 优先（下游知道自己何时放行），
// 否则指数退避加抖动。
func (r *KeyRing) coolingFor(e *keyEntry, retryAfter time.Duration) time.Duration {
	if retryAfter > 0 {
		// Retry-After 一并封顶。下游给的值不可信（bug、恶意、单位写错），无封顶
		// 意味着一把坏钥能把整个池子按住一年。真要等远超 5 分钟的，该走"弃用+人工"
		// （如 402），而不是在 429 里塞一个天文数字。
		if retryAfter > r.opts.CoolingCap {
			return r.opts.CoolingCap
		}
		return retryAfter
	}

	shift := min(e.consecutiveLimited-1, 20)
	wait := r.opts.CoolingBase << shift
	if wait > r.opts.CoolingCap || wait <= 0 {
		wait = r.opts.CoolingCap
	}
	return jitter(wait, r.opts.JitterRatio, r.opts.Rand)
}

// jitter 给时长加上 ±ratio 的随机偏移，避免多把密钥在同一毫秒到点、
// 同时冲击下游——那会把"分摊流量"变成"制造一次突发"。
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
