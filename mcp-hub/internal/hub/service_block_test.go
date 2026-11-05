package hub

import (
	"strings"
	"testing"
)

// 阈值 3 时的状态机：逐次失败跨线、成功清零、手动解封。
func TestBlockGateTransitions(t *testing.T) {
	g := newBlockGate(3)

	// 前两次失败：不拦截。
	for i := 1; i <= 2; i++ {
		ev := g.note("s1", false, "dial refused")
		if ev.Blocked || ev.JustBlocked || ev.Recovered {
			t.Fatalf("第 %d 次失败不应有任何拦截事件: %+v", i, ev)
		}
	}

	// 第三次失败：跨过阈值，进入拦截。
	ev := g.note("s1", false, "dial refused")
	if !ev.Blocked || !ev.JustBlocked {
		t.Fatalf("第 3 次失败应触发 JustBlocked: %+v", ev)
	}
	if !strings.Contains(ev.Reason, "连续 3 次") || !strings.Contains(ev.Reason, "dial refused") {
		t.Fatalf("拦截说明应含次数与详情，得到 %q", ev.Reason)
	}
	if reason, yes := g.blocked("s1"); !yes || !strings.Contains(reason, "连续 3 次") {
		t.Fatalf("blocked 应报告拦截: %q %v", reason, yes)
	}

	// 第 4 次失败：仍在拦截，但不再重复 JustBlocked（那只该响一次）。
	ev = g.note("s1", false, "timeout")
	if !ev.Blocked || ev.JustBlocked || ev.Recovered {
		t.Fatalf("第 4 次失败只应维持拦截: %+v", ev)
	}

	// 探活成功：解除拦截，计数清零。
	ev = g.note("s1", true, "")
	if !ev.Recovered || ev.Blocked {
		t.Fatalf("成功应解除拦截: %+v", ev)
	}
	if _, yes := g.blocked("s1"); yes {
		t.Fatal("成功后不应再拦截")
	}
}

// 其他 server 的成败互不影响；未失败过的 server 不拦截。
func TestBlockGateIsolation(t *testing.T) {
	g := newBlockGate(2)
	for i := 0; i < 3; i++ {
		g.note("a", false, "x")
	}
	if _, yes := g.blocked("b"); yes {
		t.Fatal("b 从未失败，不应被拦截")
	}
	if _, yes := g.blocked("a"); !yes {
		t.Fatal("a 已达阈值，应被拦截")
	}
	// b 成功不应清掉 a 的连败。
	g.note("b", true, "")
	if _, yes := g.blocked("a"); !yes {
		t.Fatal("b 的成功不应影响 a")
	}
}

// 阈值 <=0 表示功能关闭：永远不拦截。
func TestBlockGateDisabled(t *testing.T) {
	for _, threshold := range []int{0, -1} {
		g := newBlockGate(threshold)
		for i := 0; i < 10; i++ {
			if ev := g.note("s", false, "x"); ev.Blocked {
				t.Fatalf("threshold=%d 不应拦截: %+v", threshold, ev)
			}
		}
		if _, yes := g.blocked("s"); yes {
			t.Fatalf("threshold=%d 不应拦截", threshold)
		}
	}
}
