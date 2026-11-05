package tests

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hydra/mcp-hub/internal/downstream"
)

// 轮换：N 把密钥要均匀用上

func TestKeyRingRotatesEvenly(t *testing.T) {
	clock := newFakeClock()
	ring := newTestRing(t, clock, testCreds(3))

	counts := map[string]int{}
	var last string
	for i := 0; i < 9; i++ {
		lease := acquireOK(t, ring)
		lease.Done(downstream.LeaseResult{Verdict: downstream.VerdictOK})

		if id := lease.ID(); id == last {
			t.Fatalf("第 %d 次与上一次选中同一把 %s：轮询没在摊薄", i, id)
		} else {
			last = id
		}
		counts[lease.ID()]++
	}

	if len(counts) != 3 {
		t.Fatalf("只有 %d 把密钥被用到，want 3：轮询退化成了固定选一把", len(counts))
	}
	for id, n := range counts {
		if n != 3 {
			t.Errorf("%s 被选中 %d 次, want 3", id, n)
		}
	}
}

// 冷却中的密钥会被跳过，到点自动回来

func TestKeyRingSkipsCoolingKey(t *testing.T) {
	clock := newFakeClock()
	ring := newTestRing(t, clock, testCreds(3))

	first := acquireOK(t, ring)
	firstID := first.ID()
	if out := first.Done(downstream.LeaseResult{Verdict: downstream.VerdictRateLimited}); out.Cooling <= 0 {
		t.Fatalf("429 之后没有进入冷却: %+v", out)
	}

	for i := 0; i < 6; i++ {
		lease := acquireOK(t, ring)
		if lease.ID() == firstID {
			t.Fatalf("冷却中的 %s 仍被选中", firstID)
		}
		lease.Done(downstream.LeaseResult{Verdict: downstream.VerdictOK})
	}

	// 冷却到期后重新参与轮换。
	clock.Advance(2 * time.Second)
	seen := map[string]bool{}
	for i := 0; i < 6; i++ {
		lease := acquireOK(t, ring)
		seen[lease.ID()] = true
		lease.Done(downstream.LeaseResult{Verdict: downstream.VerdictOK})
	}
	if !seen[firstID] {
		t.Errorf("冷却到期后 %s 仍未回到轮换", firstID)
	}
}

// 退避：Retry-After 优先、指数递增、成功后重置

func TestKeyRingBackoffRampAndReset(t *testing.T) {
	clock := newFakeClock()
	ring := newTestRing(t, clock, testCreds(1))

	// 连续限流：1s → 2s → 4s。
	for i, want := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second} {
		lease, err := ring.Acquire(nil)
		if err != nil {
			t.Fatalf("第 %d 轮借不到密钥: %v", i+1, err)
		}
		out := lease.Done(downstream.LeaseResult{Verdict: downstream.VerdictRateLimited})
		if out.Cooling != want {
			t.Fatalf("第 %d 次退避 = %s, want %s", i+1, out.Cooling, want)
		}
		clock.Advance(want + time.Millisecond)
	}

	// 冷却到期**不**重置计数：否则会退化成 1s → 429 → 1s 的死循环。
	clock.Advance(time.Minute)
	lease, err := ring.Acquire(nil)
	if err != nil {
		t.Fatalf("冷却到期后借不到密钥: %v", err)
	}
	if out := lease.Done(downstream.LeaseResult{Verdict: downstream.VerdictRateLimited}); out.Cooling <= 4*time.Second {
		t.Errorf("冷却到期后计数被重置了：退避回落到 %s", out.Cooling)
	}

	// 一次成功才重置。
	clock.Advance(time.Minute)
	lease, err = ring.Acquire(nil)
	if err != nil {
		t.Fatalf("再次借不到密钥: %v", err)
	}
	if out := lease.Done(downstream.LeaseResult{Verdict: downstream.VerdictOK}); !out.Recovered {
		t.Errorf("冷却后的首次成功没有标记为已恢复: %+v", out)
	}

	clock.Advance(time.Minute)
	lease, err = ring.Acquire(nil)
	if err != nil {
		t.Fatalf("成功之后借不到密钥: %v", err)
	}
	if out := lease.Done(downstream.LeaseResult{Verdict: downstream.VerdictRateLimited}); out.Cooling != time.Second {
		t.Errorf("成功之后退避未重置 = %s, want 1s", out.Cooling)
	}
}

func TestKeyRingPrefersRetryAfter(t *testing.T) {
	clock := newFakeClock()
	ring := newTestRing(t, clock, testCreds(1))

	lease := acquireOK(t, ring)
	out := lease.Done(downstream.LeaseResult{
		Verdict: downstream.VerdictRateLimited,
		Obs:     downstream.Observation{StatusCode: http.StatusTooManyRequests, RetryAfter: 42 * time.Second},
	})

	// 下游说的 42 秒比自己猜的指数更权威：它知道自己什么时候放行。
	if out.Cooling != 42*time.Second {
		t.Errorf("冷却 = %s, want 42s（应当采信 Retry-After）", out.Cooling)
	}
}

// 401 弃用，且重启后仍是弃用

func TestKeyRingInvalidKeyDisables(t *testing.T) {
	clock := newFakeClock()
	ring := newTestRing(t, clock, testCreds(1))

	lease := acquireOK(t, ring)
	out := lease.Done(downstream.LeaseResult{
		Verdict: downstream.VerdictInvalidKey,
		Obs:     downstream.Observation{StatusCode: http.StatusUnauthorized},
	})
	if !out.Disabled {
		t.Fatalf("401 之后密钥未被弃用: %+v", out)
	}

	if _, err := ring.Acquire(nil); err == nil {
		t.Error("已被弃用的密钥仍能被借出")
	}

	// 重启语义：disabled 是持久态，随输入一起回来。
	restarted := newTestRing(t, clock, []downstream.CredentialInput{{
		ID: "key-1", Label: "密钥 1",
		Headers: map[string]string{"Authorization": "Bearer secret-1"},
		Enabled: true, State: downstream.KeyDisabled,
	}})
	if _, err := restarted.Acquire(nil); err == nil {
		t.Error("重启后仍在使用一把已知无效的密钥")
	}
}

// 403 给两次机会

func TestKeyRingRevokedNeedsTwoStrikes(t *testing.T) {
	clock := newFakeClock()
	ring := newTestRing(t, clock, testCreds(1))

	first := acquireOK(t, ring)
	out1 := first.Done(downstream.LeaseResult{
		Verdict: downstream.VerdictKeyRevoked,
		Obs:     downstream.Observation{StatusCode: http.StatusForbidden},
	})
	if out1.Disabled {
		t.Fatalf("403 第一次就把密钥弃用了：403 也可能是 IP 白名单或 scope 问题，换密钥没用")
	}
	if !strings.Contains(out1.Detail, "403") {
		t.Errorf("未说明这是第二次机会: %q", out1.Detail)
	}

	second := acquireOK(t, ring)
	out2 := second.Done(downstream.LeaseResult{
		Verdict: downstream.VerdictKeyRevoked,
		Obs:     downstream.Observation{StatusCode: http.StatusForbidden},
	})
	if !out2.Disabled {
		t.Errorf("连续两次 403 之后仍未弃用: %+v", out2)
	}
}

// 全部冷却时快速失败，并给出可执行的 retryAfter

func TestKeyRingAllCoolingFailsFast(t *testing.T) {
	clock := newFakeClock()
	ring := newTestRing(t, clock, testCreds(3))

	for i := 0; i < 3; i++ {
		lease := acquireOK(t, ring)
		lease.Done(downstream.LeaseResult{
			Verdict: downstream.VerdictRateLimited,
			Obs:     downstream.Observation{RetryAfter: 30 * time.Second},
		})
	}

	_, err := ring.Acquire(nil)
	noKey, ok := err.(*downstream.ErrNoUsableKey)
	if !ok {
		t.Fatalf("全部冷却时错误类型 = %T(%v), want *ErrNoUsableKey", err, err)
	}
	if noKey.RetryAfter <= 0 {
		t.Errorf("没有给出 retryAfter，调用方只能瞎猜: %+v", noKey)
	}
	if noKey.Cooling != 3 {
		t.Errorf("冷却中的计数 = %d, want 3: %+v", noKey.Cooling, noKey)
	}
}

// 5xx 与超时让密钥状态零变化

func TestKeyRingNeutralFailureChangesNothing(t *testing.T) {
	clock := newFakeClock()
	ring := newTestRing(t, clock, testCreds(2))

	for _, obs := range []downstream.Observation{
		{StatusCode: http.StatusInternalServerError},
		{Err: context.DeadlineExceeded},
	} {
		lease := acquireOK(t, ring)
		id := lease.ID()
		lease.Done(downstream.LeaseResult{Verdict: downstream.VerdictNeutral, Obs: obs})

		st, ok := ring.StatusOf(id)
		if !ok {
			t.Fatalf("取不到 %s 的状态", id)
		}
		if st.State != downstream.KeyActive {
			t.Errorf("5xx/超时之后 %s 的状态变成了 %s：这两个信号与密钥无关", id, st.State)
		}
		if st.ConsecutiveLimited != 0 {
			t.Errorf("%s 的退避计数被 5xx/超时推进了：%d", id, st.ConsecutiveLimited)
		}
	}
}

// 集体失败保护：别把用户的密钥逐个禁用成一片红

func TestKeyRingCollectiveFailureProtection(t *testing.T) {
	clock := newFakeClock()
	ring := newTestRing(t, clock, testCreds(2))

	// 第一把无效：此时还有另一把可用，正常弃用。
	first := acquireOK(t, ring)
	if out := first.Done(downstream.LeaseResult{Verdict: downstream.VerdictInvalidKey}); !out.Disabled {
		t.Fatalf("在还有备用密钥时没有弃用无效密钥: %+v", out)
	}

	// 第二把也无效：禁用它会让可用密钥归零，且已有同因失败。
	second := acquireOK(t, ring)
	out := second.Done(downstream.LeaseResult{Verdict: downstream.VerdictInvalidKey})
	if out.Disabled {
		t.Fatal("弃用最后一把密钥会让可用数归零，应当被集体失败保护拦下")
	}
	if !out.CollectiveProtection {
		t.Errorf("未标记为集体失败保护: %+v", out)
	}

	// 关键结果：还剩一把可用，而不是一片红。
	if n := ring.UsableCount(); n != 1 {
		t.Errorf("可用密钥数 = %d, want 1", n)
	}
}

// RPM 超额后流量转到下一把，窗口滚动即恢复

func TestKeyRingRPMExhaustionSpillsOver(t *testing.T) {
	clock := newFakeClock()
	creds := []downstream.CredentialInput{
		{ID: "key-1", Label: "A", Headers: map[string]string{"Authorization": "Bearer a"},
			Enabled: true, State: downstream.KeyActive, RPMLimit: 2},
		{ID: "key-2", Label: "B", Headers: map[string]string{"Authorization": "Bearer b"},
			Enabled: true, State: downstream.KeyActive, RPMLimit: 2},
	}
	ring := newTestRing(t, clock, creds)

	counts := map[string]int{}
	for i := 0; i < 4; i++ {
		lease := acquireOK(t, ring)
		counts[lease.ID()]++
		lease.Done(downstream.LeaseResult{Verdict: downstream.VerdictOK})
	}
	for id, n := range counts {
		if n != 2 {
			t.Errorf("%s 在 RPM=2 下被用了 %d 次", id, n)
		}
	}

	_, err := ring.Acquire(nil)
	noKey, ok := err.(*downstream.ErrNoUsableKey)
	if !ok {
		t.Fatalf("配额用尽后仍借到了密钥: %v", err)
	}
	if noKey.Exhausted != 2 {
		t.Errorf("配额用尽的计数 = %d, want 2: %+v", noKey.Exhausted, noKey)
	}

	clock.Advance(time.Minute)
	if _, err := ring.Acquire(nil); err != nil {
		t.Errorf("窗口滚动后仍未恢复: %v", err)
	}
}

// 重启语义：cooling 复位、disabled 保持

func TestKeyRingRestartSemantics(t *testing.T) {
	clock := newFakeClock()
	ring := newTestRing(t, clock, testCreds(1))

	lease := acquireOK(t, ring)
	lease.Done(downstream.LeaseResult{
		Verdict: downstream.VerdictRateLimited,
		Obs:     downstream.Observation{RetryAfter: time.Hour},
	})
	if _, err := ring.Acquire(nil); err == nil {
		t.Fatal("限流中的密钥仍可借出")
	}

	// 进程重启：库里的状态还是 active（cooling 从不落库）。重启往往就是为了
	// 从限流里恢复，把限流状态持久化只会让新进程背旧账。
	restarted := newTestRing(t, clock, testCreds(1))
	if _, err := restarted.Acquire(nil); err != nil {
		t.Errorf("重启后仍被限流状态挡住: %v", err)
	}
}

// Sync 保留运行态

func TestKeyRingSyncKeepsRuntimeState(t *testing.T) {
	clock := newFakeClock()
	ring := newTestRing(t, clock, testCreds(2))

	lease := acquireOK(t, ring)
	id := lease.ID()
	lease.Done(downstream.LeaseResult{
		Verdict: downstream.VerdictRateLimited,
		Obs:     downstream.Observation{RetryAfter: time.Hour},
	})

	// 改一次 label（最常见的"顺手改配置"），冷却不该被清掉：否则等于开了个
	// "点一下即可绕过限流"的后门。
	creds := testCreds(2)
	for i := range creds {
		creds[i].Label = "改名 " + creds[i].Label
	}
	ring.Sync(creds)

	st, _ := ring.StatusOf(id)
	if st.State != downstream.KeyCooling {
		t.Errorf("Sync 之后 %s 的冷却被清掉了: state=%s", id, st.State)
	}
}

// 指纹：能区分，但不泄露

func TestKeyFingerprintDoesNotLeakAndDistinguishes(t *testing.T) {
	a := downstream.KeyFingerprint(map[string]string{"Authorization": "Bearer alpha-secret"})
	b := downstream.KeyFingerprint(map[string]string{"Authorization": "Bearer beta-secret"})
	same := downstream.KeyFingerprint(map[string]string{"Authorization": "Bearer alpha-secret"})

	if a == b {
		t.Error("两把不同的密钥指纹相同，用户无法在界面上区分它们")
	}
	if a != same {
		t.Error("同一把密钥的指纹不稳定")
	}
	if strings.Contains(a, "alpha") || strings.Contains(a, "secret") {
		t.Errorf("指纹泄露了密钥内容: %q", a)
	}
	if len(a) > 8 {
		t.Errorf("指纹太长（%q）：它的作用只是区分，不是校验", a)
	}
}

// 判决映射与重试边界

func TestVerdictOfMapping(t *testing.T) {
	cases := []struct {
		name string
		kind downstream.ErrorKind
		obs  downstream.Observation
		want downstream.KeyVerdict
	}{
		{"成功", downstream.KindNone, downstream.Observation{StatusCode: 200}, downstream.VerdictOK},
		{"429", downstream.KindRateLimited, downstream.Observation{StatusCode: 429}, downstream.VerdictRateLimited},
		{"402", downstream.KindQuotaExhausted, downstream.Observation{StatusCode: 402}, downstream.VerdictQuotaExhausted},
		{"401", downstream.KindAuthRequired, downstream.Observation{StatusCode: 401}, downstream.VerdictInvalidKey},
		{"403", downstream.KindAuthRequired, downstream.Observation{StatusCode: 403}, downstream.VerdictKeyRevoked},
		{"5xx", downstream.KindUpstreamError, downstream.Observation{StatusCode: 500}, downstream.VerdictNeutral},
		{"超时", downstream.KindTimeout, downstream.Observation{}, downstream.VerdictNeutral},
		{"DNS", downstream.KindDNSFail, downstream.Observation{}, downstream.VerdictNeutral},
		{"业务错误", downstream.KindUnknown, downstream.Observation{StatusCode: 200}, downstream.VerdictNeutral},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := downstream.VerdictOf(tc.kind, tc.obs); got != tc.want {
				t.Errorf("VerdictOf(%s) = %s, want %s", tc.kind, got, tc.want)
			}
		})
	}
}

func TestKeyVerdictRetryBoundary(t *testing.T) {
	// 可重试 = 确定请求未被执行。这四类都满足。
	for _, v := range []downstream.KeyVerdict{
		downstream.VerdictRateLimited, downstream.VerdictQuotaExhausted,
		downstream.VerdictInvalidKey, downstream.VerdictKeyRevoked,
	} {
		if !v.Retryable() {
			t.Errorf("%s 应当允许换密钥重试", v)
		}
	}
	// 中性失败可能已经执行过：重试会让有副作用的工具跑第二遍。
	for _, v := range []downstream.KeyVerdict{downstream.VerdictNeutral, downstream.VerdictOK} {
		if v.Retryable() {
			t.Errorf("%s 不该允许重试：请求可能已经执行", v)
		}
	}
}

// 密钥池打空要有自己的分类

// NO_USABLE_KEY 如果没有人产生，用户就会看到"未知错误"：一段本可执行的说明
// （还有几把在冷却、约多久后重试）被降级成一句无用的"稍后重试即可"。
// 这条守的是分类接线，不是分类定义。
func TestNoUsableKeyHasItsOwnKind(t *testing.T) {
	noKey := &downstream.ErrNoUsableKey{
		ServerKey:  "s",
		Total:      3,
		Cooling:    2,
		Disabled:   1,
		RetryAfter: 30 * time.Second,
	}

	if got := downstream.CallErrorKind(noKey); got != downstream.KindNoUsableKey {
		t.Errorf("CallErrorKind = %s, want NO_USABLE_KEY", got)
	}
	// 上层会把它包一层（比如预算耗尽时挂 Cause），包装后仍要认得出。
	wrapped := fmt.Errorf("调用失败: %w", noKey)
	if got := downstream.CallErrorKind(wrapped); got != downstream.KindNoUsableKey {
		t.Errorf("包装后的 CallErrorKind = %s, want NO_USABLE_KEY", got)
	}

	// 说明文字本身要带上用户唯一能用的那两个数字。
	text := noKey.Error()
	for _, want := range []string{"3", "2", "30s"} {
		if !strings.Contains(text, want) {
			t.Errorf("说明里缺少 %q: %s", want, text)
		}
	}
}

// 在飞期间被人工停用的密钥，429 反馈不得使其复活

// 时间线：借出密钥 → 管理端停用该密钥（Sync 写入 disabled）→ 在飞调用收到
// 429 → settle。若 settle 顺手把 state 写回 active，冷却一结束密钥就复活，
// 界面与库记录从此相反，且会反复被选中、反复被下游拒绝。
func TestSettleRateLimitedKeepsManualDisable(t *testing.T) {
	clock := newFakeClock()
	creds := testCreds(2)
	ring := newTestRing(t, clock, creds)

	lease := acquireOK(t, ring)
	id := lease.ID()

	// 调用在飞期间，该密钥被人工停用。
	for i, c := range creds {
		if c.ID == id {
			creds[i].Enabled = false
			creds[i].State = downstream.KeyDisabled
			creds[i].Disabled = downstream.DisabledInfo{
				Kind: downstream.VerdictInvalidKey, Note: "人工停用",
			}
		}
	}
	ring.Sync(creds)

	out := lease.Done(downstream.LeaseResult{Verdict: downstream.VerdictRateLimited})
	if out.Cooling <= 0 {
		t.Fatalf("429 应当照旧进入冷却: %+v", out)
	}

	st, ok := ring.StatusOf(id)
	if !ok {
		t.Fatal("密钥不见了")
	}
	if st.State != downstream.KeyDisabled {
		t.Errorf("429 结算后 state = %s, want disabled: 在飞期间的停用被抹掉了", st.State)
	}

	// 冷却结束后也不得复活。
	clock.Advance(out.Cooling + time.Second)
	st, _ = ring.StatusOf(id)
	if st.State != downstream.KeyDisabled {
		t.Errorf("冷却结束后 state = %s, want disabled", st.State)
	}
	if ring.UsableCount() != 1 {
		t.Errorf("UsableCount = %d, want 1：被停用的密钥又回到了轮换里", ring.UsableCount())
	}
}

// 并发 Sync + Acquire：租约必须持有配置快照

// 管理端（改密钥头、启停、重载）调 Sync，在锁内改写 entry 的 label 与 headers；
// 调用方在 Acquire 返回后不持锁地读 lease 的头。直接读 entry 会被 -race 抓到，
// 本用例须用 `go test -race` 运行。
func TestKeyLeaseIsolatesFromConcurrentSync(t *testing.T) {
	clock := newFakeClock()
	ring := newTestRing(t, clock, testCreds(2))

	var wg sync.WaitGroup
	stop := make(chan struct{})

	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			creds := make([]downstream.CredentialInput, 0, 2)
			for j := 0; j < 2; j++ {
				creds = append(creds, downstream.CredentialInput{
					ID:      fmt.Sprintf("key-%d", j+1),
					Label:   fmt.Sprintf("密钥 %d 第 %d 版", j+1, i),
					Headers: map[string]string{"Authorization": fmt.Sprintf("Bearer v%d-%d", i, j+1)},
					Enabled: true,
					State:   downstream.KeyActive,
				})
			}
			ring.Sync(creds)
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			lease, err := ring.Acquire(nil)
			if err != nil {
				continue
			}
			// Acquire 返回后锁已释放：这里的读必须与上面的 Sync 无竞争。
			hdrs := lease.Headers()
			lbl := lease.Label()
			if len(hdrs) == 0 || lbl == "" {
				t.Errorf("第 %d 次租约快照为空: hdrs=%v label=%q", i, hdrs, lbl)
			}
			lease.Done(downstream.LeaseResult{Verdict: downstream.VerdictOK})
		}
	}()

	time.Sleep(300 * time.Millisecond)
	close(stop)
	wg.Wait()
}
