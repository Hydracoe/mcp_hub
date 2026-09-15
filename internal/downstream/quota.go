package downstream

import "time"

// windowPolicy 决定"本次计量值为 0"时怎么处理。
type windowPolicy int

const (
	// windowReject 按计数限制。RPM 用它：一次请求就是 1，没有含糊。
	windowReject windowPolicy = iota

	// windowIgnoreWhenUnmetered 在无法计量时直接放行。
	//
	// TPM 用它：网关发请求前拿不到 token 数，用户又没配预估时，
	// "按 0 计入"与"放行不计量"看起来一样，实际差很远——前者会让
	// 界面显示 0/100k 一片祥和，后者坦率地说"没在算"。
	windowIgnoreWhenUnmetered
)

// quotaWindow 是固定窗口计数器，用于 RPM / TPM 限速。
//
// 为什么是固定窗口而不是滑动窗口或令牌桶：窗口边界对齐到分钟，
// 与供应商侧的配额窗口语义一致，也能直接算出"下个窗口何时开始"——
// 这正是快速失败时要回给调用方的 retryAfter。滑动窗口更平滑，
// 但给不出一个确定的恢复时刻。
//
// 并发：自身不加锁，由 KeyRing.mu 保护。
type quotaWindow struct {
	limit  int
	window time.Duration
	policy windowPolicy

	start time.Time
	used  int
}

// setLimit 更新上限。已有的计数保留：用户把上限调大，不该顺带
// 把已经用掉的量清掉。
func (w *quotaWindow) setLimit(n int) {
	if w.window <= 0 {
		w.window = time.Minute
	}
	w.limit = n
}

// roll 在窗口翻转时重置计数。
//
// 超发（预估不足导致 used > limit）的部分转入下一窗口，**不豁免**：
// 预估失准是网关自己的问题，让下游承担额外流量是另一回事。
// 反方向的剩余额度不滚存——把没用满的额度攒起来，等于在
// 某几个窗口里放行超出配置上限的流量。
func (w *quotaWindow) roll(now time.Time) {
	if w.limit <= 0 || w.window <= 0 {
		return
	}
	slot := now.Truncate(w.window)
	if w.start.IsZero() {
		w.start = slot
		return
	}
	if !slot.After(w.start) {
		return
	}
	if overflow := w.used - w.limit; overflow > 0 {
		w.used = overflow
	} else {
		w.used = 0
	}
	w.start = slot
}

// allow 判断能否放行 want 个单位的用量，不能时给出恢复到可用的等待时长。
func (w *quotaWindow) allow(now time.Time, want int) (bool, time.Duration) {
	if w.limit <= 0 {
		return true, 0
	}
	if want <= 0 {
		if w.policy == windowIgnoreWhenUnmetered {
			return true, 0
		}
		want = 0
	}

	w.roll(now)
	if w.used+want > w.limit {
		return false, w.windowEnd(now).Sub(now)
	}
	return true, 0
}

// precharge 在放行时预先计入估计用量。
func (w *quotaWindow) precharge(now time.Time, n int) {
	if w.limit <= 0 || n <= 0 {
		return
	}
	w.roll(now)
	w.used += n
}

// settle 在拿到真实用量后结算差额。
//
// actual < 0 表示下游没有声明用量，保持预扣值——这就是 TPM
// "尽力而为"的可观测含义：计量要么来自响应，要么来自预估，
// 两者都没有时它就是一个纯放行的窗口。
func (w *quotaWindow) settle(now time.Time, est, actual int) {
	if w.limit <= 0 || actual < 0 {
		return
	}
	w.roll(now)
	w.used += actual - est
	if w.used < 0 {
		w.used = 0
	}
}

// reset 清零计数，用于人工"重置配额"。
func (w *quotaWindow) reset() {
	w.used = 0
	w.start = time.Time{}
}

// snapshot 返回当前用量与上限。
func (w *quotaWindow) snapshot(now time.Time) (int, int) {
	if w.limit <= 0 {
		return 0, 0
	}
	w.roll(now)
	return w.used, w.limit
}

// windowEnd 返回当前窗口的结束时刻。
func (w *quotaWindow) windowEnd(now time.Time) time.Time {
	if w.window <= 0 {
		return now
	}
	return now.Truncate(w.window).Add(w.window)
}

// nextRefill 返回"还要等多久配额才有余量"。
//
// 只在当前已用满时才给正值：没用满却报一个等待时长，会让
// "全部密钥都不可用"的 retryAfter 变得没有意义。
func (w *quotaWindow) nextRefill(now time.Time) time.Duration {
	if w.limit <= 0 {
		return 0
	}
	w.roll(now)
	if w.used < w.limit {
		return 0
	}
	return w.windowEnd(now).Sub(now)
}
