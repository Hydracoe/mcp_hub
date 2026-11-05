package tests

import (
	"testing"
	"time"

	"github.com/hydra/mcp-hub/internal/downstream"
)

// --- 人工解除冷却：冷却与"进过冷却"的标记一起复位 ---

// 人工解除冷却之后，密钥是被人放回池子的，不是自己回来的。因此
// "冷却后的首次成功"这个信号（Recovered）不该再报出来——
// 否则"它自己恢复了"与"人把它捞回来了"在事件流里分不开。
func TestClearCoolingResetsRecoveredSignal(t *testing.T) {
	clock := newFakeClock()
	creds := []downstream.CredentialInput{{
		ID:      "key-1",
		Label:   "密钥 1",
		Headers: map[string]string{"Authorization": "Bearer secret-1"},
		Enabled: true,
		State:   downstream.KeyActive,
	}}
	ring := newTestRing(t, clock, creds)

	// 先把它打进冷却。
	lease := acquireOK(t, ring)
	out := lease.Done(downstream.LeaseResult{Verdict: downstream.VerdictRateLimited})
	if out.Cooling <= 0 {
		t.Fatalf("429 之后没有进入冷却: %+v", out)
	}

	// 人工解除冷却。
	if !ring.ClearCooling("key-1") {
		t.Fatal("ClearCooling 返回 false：密钥不存在")
	}

	st, ok := ring.StatusOf("key-1")
	if !ok {
		t.Fatal("StatusOf 没找到密钥")
	}
	if st.State != downstream.KeyActive {
		t.Errorf("状态 = %s, want %s：人工解除冷却后它还在冷却", st.State, downstream.KeyActive)
	}
	if st.ConsecutiveLimited != 1 {
		t.Errorf("连续限流次数 = %d, want 1：解除冷却不该顺手清掉退避计数（M8：人工不清计数）",
			st.ConsecutiveLimited)
	}

	// 冷却已经解除，接下来那次成功不是"冷却后的首次成功"。
	lease = acquireOK(t, ring)
	out = lease.Done(downstream.LeaseResult{Verdict: downstream.VerdictOK})
	if out.Recovered {
		t.Errorf("人工解除冷却后仍报 Recovered（%+v）：hadCooling 没随冷却一起复位", out)
	}
}

// 复位不等于把"进过冷却"这件事永久抹掉：之后真的再被限流一次，
// 冷却后的首次成功仍然要报 Recovered。
func TestClearCoolingKeepsRecoveredSignalForLaterCooling(t *testing.T) {
	clock := newFakeClock()
	creds := []downstream.CredentialInput{{
		ID:      "key-1",
		Label:   "密钥 1",
		Headers: map[string]string{"Authorization": "Bearer secret-1"},
		Enabled: true,
		State:   downstream.KeyActive,
	}}
	ring := newTestRing(t, clock, creds)

	lease := acquireOK(t, ring)
	lease.Done(downstream.LeaseResult{Verdict: downstream.VerdictRateLimited})
	if !ring.ClearCooling("key-1") {
		t.Fatal("ClearCooling 返回 false：密钥不存在")
	}

	// 再被限流一次（连续计数到 2，退避 2s）。
	lease = acquireOK(t, ring)
	lease.Done(downstream.LeaseResult{Verdict: downstream.VerdictRateLimited})

	// 这次冷却到期后的首次成功仍要报 Recovered。
	clock.Advance(3 * time.Second)
	lease = acquireOK(t, ring)
	out := lease.Done(downstream.LeaseResult{Verdict: downstream.VerdictOK})
	if !out.Recovered {
		t.Errorf("再次冷却后的首次成功没有标记 Recovered: %+v", out)
	}
}
