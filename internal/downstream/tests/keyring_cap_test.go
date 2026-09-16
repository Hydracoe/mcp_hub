package tests

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hydra/mcp-hub/internal/downstream"
)

// --- R16：连续限流顶格 → 自动弃用并通知人工 ---

func TestKeyRingRateLimitedCapDisables(t *testing.T) {
	clock := newFakeClock()
	ring := newTestRing(t, clock, testCreds(1))

	var last downstream.KeyOutcome
	for i := 0; i < 10; i++ {
		lease := acquireOK(t, ring)
		last = lease.Done(downstream.LeaseResult{Verdict: downstream.VerdictRateLimited})
		if i < 9 && last.Disabled {
			t.Fatalf("第 %d 次就被弃用，未到阈值", i+1)
		}
		// 推过冷却窗口，保证下一轮还能借到这把钥（顶格退避最长 5 分钟）。
		clock.Advance(10 * time.Minute)
	}
	if !last.Disabled {
		t.Fatalf("连续 %d 次 429 仍未弃用：无限循环没有被打破", last.ConsecutiveLimited)
	}
	if last.State != downstream.KeyDisabled {
		t.Fatalf("out.State = %s, want disabled", last.State)
	}
	if last.Cooling != 0 {
		t.Errorf("弃用后不该再带冷却时长: %s", last.Cooling)
	}
	if !strings.Contains(last.Detail, "账号级限流") {
		t.Fatalf("弃用原因应写明疑似账号级限流: %q", last.Detail)
	}

	// 弃用后立刻借不到，且错误文案指向人工处理（总原则③）。
	_, err := ring.Acquire(nil)
	var noKey *downstream.ErrNoUsableKey
	if !errors.As(err, &noKey) {
		t.Fatalf("弃用后 Acquire 应返回 ErrNoUsableKey，got %v", err)
	}
	if !strings.Contains(noKey.Error(), "需要人工处理") {
		t.Fatalf("纯弃用场景文案缺人工指引: %q", noKey.Error())
	}
	if strings.Contains(noKey.Error(), "后重试") {
		t.Fatalf("纯弃用场景不该出现\"重试\"话术: %q", noKey.Error())
	}
}

// --- R16 × R11：整池同时顶格是账号/配置问题，集体保护不全禁 ---

func TestKeyRingCapDisableKeepsCollectiveProtection(t *testing.T) {
	clock := newFakeClock()
	creds := testCreds(2)
	// 阈值调成 1：一次 429 就顶格，好造"整池同时顶格"。
	ring := newTestRing(t, clock, creds, func(o *downstream.KeyRingOptions) {
		o.LimitedDisableThreshold = 1
	})

	// 第一把被顶格禁用后（此时第二把还可用，保护不触发），推进时钟让
	// 第二把也顶格：此刻弃用它会导致可用归零，且第一把在"同因窗口"内
	// 因同一原因失败——应被集体保护拦下。窗口对限流类放宽到
	// 2×CoolingCap=10 分钟，这里推进 9 分钟落在窗口内。
	first := acquireOK(t, ring)
	first.Done(downstream.LeaseResult{Verdict: downstream.VerdictRateLimited})
	clock.Advance(9 * time.Minute)

	second := acquireOK(t, ring)
	out := second.Done(downstream.LeaseResult{Verdict: downstream.VerdictRateLimited})
	if !out.CollectiveProtection {
		t.Fatalf("整池同时顶格应触发集体失败保护: %+v", out)
	}
	if out.Disabled {
		t.Fatalf("集体保护下密钥不应被弃用: %+v", out)
	}
}

// --- R5：Retry-After 一并封顶，夸张值不能再锁死密钥 ---

func TestKeyRingRetryAfterIsCapped(t *testing.T) {
	clock := newFakeClock()
	ring := newTestRing(t, clock, testCreds(1))

	lease := acquireOK(t, ring)
	out := lease.Done(downstream.LeaseResult{
		Verdict: downstream.VerdictRateLimited,
		Obs:     downstream.Observation{RetryAfter: 365 * 24 * time.Hour},
	})
	if out.Cooling != 5*time.Minute {
		t.Fatalf("夸张 Retry-After 未被封顶: got %s, want 5m", out.Cooling)
	}
}

// --- 混合场景：冷却（自动恢复）与弃用（需人工）同时存在，话术要分开说 ---

func TestNoUsableKeyCopyDistinguishesRecoveryFromHuman(t *testing.T) {
	clock := newFakeClock()
	ring := newTestRing(t, clock, testCreds(2))

	a := acquireOK(t, ring)
	a.Done(downstream.LeaseResult{Verdict: downstream.VerdictRateLimited})
	b := acquireOK(t, ring)
	b.Done(downstream.LeaseResult{Verdict: downstream.VerdictQuotaExhausted})

	_, err := ring.Acquire(nil)
	var noKey *downstream.ErrNoUsableKey
	if !errors.As(err, &noKey) {
		t.Fatalf("应返回 ErrNoUsableKey，got %v", err)
	}
	msg := noKey.Error()
	if !strings.Contains(msg, "后重试") {
		t.Errorf("混合场景应给出可等待的恢复时间（冷却钥会回来）: %q", msg)
	}
	if !strings.Contains(msg, "不会自动恢复") {
		t.Errorf("混合场景应说明弃用钥不会自动恢复: %q", msg)
	}
}
