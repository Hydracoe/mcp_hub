package downstream

import "time"

// quotaWindow 是固定窗口计数器，用于 RPM 限速。
// 窗口边界对齐到分钟，与供应商语义一致，并能算出下个窗口何时开始（即 retryAfter）。
// 一次请求记 1；自身不加锁，由 KeyRing.mu 保护。
type quotaWindow struct {
	limit  int
	window time.Duration

	start time.Time
	used  int
}

// setLimit 更新上限。已有计数保留：用户把上限调大，不该顺带把已用掉的量清掉。
func (w *quotaWindow) setLimit(n int) {
	if w.window <= 0 {
		w.window = time.Minute
	}
	w.limit = n
}

// roll 在窗口翻转时重置计数。
// 超发（并发下短暂的 used > limit）的部分转入下一窗口，不豁免：漏算的额度
// 不滚存，否则等于在某几个窗口里放行超出配置上限的流量。
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
	if want < 0 {
		want = 0
	}

	w.roll(now)
	if w.used+want > w.limit {
		return false, w.windowEnd(now).Sub(now)
	}
	return true, 0
}

// precharge 在放行时预先计入用量。
func (w *quotaWindow) precharge(now time.Time, n int) {
	if w.limit <= 0 || n <= 0 {
		return
	}
	w.roll(now)
	w.used += n
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
