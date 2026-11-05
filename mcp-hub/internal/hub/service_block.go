package hub

import (
	"fmt"
	"sync"
)

// blockEvent 是一次探活结果对拦截状态的影响。
type blockEvent struct {
	// Blocked 报告该 server 此刻是否处于拦截态。
	Blocked bool
	// JustBlocked 表示这次失败刚好跨过阈值（从可调用变为拦截）。
	JustBlocked bool
	// Recovered 表示拦截状态因本次探活成功而解除。
	Recovered bool
	// Streak 是当前连续失败次数（成功后为 0）。
	Streak int
	// Reason 是拦截说明，进调用方报错文案与日志。
	Reason string
}

// blockGate 跟踪每个 server 的连续探活失败次数，达到阈值后拦截其工具调用。
// 探活本身永不停止，这里只影响"能不能调"，不影响"探不探"。
// 只存内存：重启即清零，由后续失败重新累计。
type blockGate struct {
	// threshold 是拦截阈值：连续失败达到该次数后拦截。
	// <=0 表示功能关闭，一切探活结果都不影响调用。
	threshold int

	mu      sync.Mutex
	streaks map[string]int
	details map[string]string // 每个 server 最近一次失败的详情，进拦截文案
}

func newBlockGate(threshold int) *blockGate {
	return &blockGate{
		threshold: threshold,
		streaks:   make(map[string]int),
		details:   make(map[string]string),
	}
}

// note 记录一次探活结果；所有落库的探活结果（含手动触发）都应经过这里。
func (g *blockGate) note(serverID string, ok bool, detail string) blockEvent {
	if g.threshold <= 0 {
		return blockEvent{}
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	if ok {
		prev := g.streaks[serverID]
		delete(g.streaks, serverID)
		delete(g.details, serverID)
		return blockEvent{Streak: 0, Recovered: prev >= g.threshold}
	}

	g.streaks[serverID]++
	streak := g.streaks[serverID]
	if detail != "" {
		g.details[serverID] = detail
	}
	blocked := streak >= g.threshold
	ev := blockEvent{
		Blocked:     blocked,
		JustBlocked: blocked && streak == g.threshold,
		Streak:      streak,
	}
	if blocked {
		ev.Reason = fmt.Sprintf("连续 %d 次探活失败，最近一次：%s", streak, g.details[serverID])
	}
	return ev
}

// reset 清除某个 server 的连败记录。目标地址变更后必须调用：
// 旧地址的失败不得用于拦截新地址的调用。
func (g *blockGate) reset(serverID string) {
	g.mu.Lock()
	delete(g.streaks, serverID)
	delete(g.details, serverID)
	g.mu.Unlock()
}

// blocked 报告该 server 是否被拦截，给调用路径用。调用路径高频，
// 必须只读、无副作用。
func (g *blockGate) blocked(serverID string) (string, bool) {
	if g.threshold <= 0 {
		return "", false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	streak, ok := g.streaks[serverID]
	if !ok || streak < g.threshold {
		return "", false
	}
	return fmt.Sprintf("连续 %d 次探活失败，最近一次：%s", streak, g.details[serverID]), true
}
