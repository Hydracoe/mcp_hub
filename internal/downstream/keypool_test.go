package downstream

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeClock 让退避与冷却在测试里可被推进。
//
// 不注入时钟的话，验证"1s→2s→4s 递增"这种语义就得真的睡 7 秒，
// 而一旦真的睡下去，测试会因为慢而被跳过——跳过的那部分恰好是
// 退避逻辑，也就是最容易写错的那部分。
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// testCreds 造 n 把互不相同的密钥。
func testCreds(n int) []CredentialInput {
	out := make([]CredentialInput, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, CredentialInput{
			ID:      fmt.Sprintf("key-%d", i+1),
			Label:   fmt.Sprintf("密钥 %d", i+1),
			Headers: map[string]string{"Authorization": fmt.Sprintf("Bearer secret-%d", i+1)},
			Enabled: true,
			State:   KeyActive,
		})
	}
	return out
}

// newTestRing 构造一个关了抖动、时间可控的密钥池。
func newTestRing(t *testing.T, clock *fakeClock, creds []CredentialInput, tune ...func(*KeyRingOptions)) *KeyRing {
	t.Helper()
	opts := KeyRingOptions{
		ServerID:    "srv-1",
		ServerKey:   "demo",
		JitterRatio: -1, // 显式关闭抖动：测试要的是确定性
		Now:         clock.Now,
	}
	for _, fn := range tune {
		fn(&opts)
	}
	return NewKeyRing(opts, creds)
}

// acquireOK 借一把密钥，失败即测试失败。
func acquireOK(t *testing.T, r *KeyRing) *KeyLease {
	t.Helper()
	lease, err := r.Acquire(nil)
	if err != nil {
		t.Fatalf("Acquire 失败: %v", err)
	}
	return lease
}

// --- §7.5.12 用例 1：N 把密钥轮换 ---

func TestKeyRingRotatesEvenly(t *testing.T) {
	clock := newFakeClock()
	ring := newTestRing(t, clock, testCreds(3))

	counts := map[string]int{}
	var last string
	for i := 0; i < 9; i++ {
		lease := acquireOK(t, ring)
		lease.Done(LeaseResult{Verdict: VerdictOK})

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

// --- 用例 2：冷却中的密钥被跳过 ---

func TestKeyRingSkipsCoolingKey(t *testing.T) {
	clock := newFakeClock()
	ring := newTestRing(t, clock, testCreds(3))

	// 第一把被限流，进入冷却。
	first := acquireOK(t, ring)
	firstID := first.ID()
	if out := first.Done(LeaseResult{Verdict: VerdictRateLimited}); out.Cooling <= 0 {
		t.Fatalf("429 之后没有进入冷却: %+v", out)
	}

	for i := 0; i < 6; i++ {
		lease := acquireOK(t, ring)
		if lease.ID() == firstID {
			t.Fatalf("冷却中的 %s 仍被选中", firstID)
		}
		lease.Done(LeaseResult{Verdict: VerdictOK})
	}

	// 冷却到期后重新参与轮换。
	clock.Advance(2 * time.Second)
	seen := map[string]bool{}
	for i := 0; i < 6; i++ {
		lease := acquireOK(t, ring)
		seen[lease.ID()] = true
		lease.Done(LeaseResult{Verdict: VerdictOK})
	}
	if !seen[firstID] {
		t.Errorf("冷却到期后 %s 仍未回到轮换", firstID)
	}
}

// --- 用例 3：Retry-After 优先，指数退避递增，成功后重置 ---

func TestKeyRingBackoffRampAndReset(t *testing.T) {
	clock := newFakeClock()
	ring := newTestRing(t, clock, testCreds(1))

	// 连续限流：1s → 2s → 4s。
	for i, want := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second} {
		lease, err := ring.Acquire(nil)
		if err != nil {
			t.Fatalf("第 %d 轮借不到密钥: %v", i+1, err)
		}
		out := lease.Done(LeaseResult{Verdict: VerdictRateLimited})
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
	if out := lease.Done(LeaseResult{Verdict: VerdictRateLimited}); out.Cooling <= 4*time.Second {
		t.Errorf("冷却到期后计数被重置了：退避回落到 %s", out.Cooling)
	}

	// 一次成功才重置。
	clock.Advance(time.Minute)
	lease, err = ring.Acquire(nil)
	if err != nil {
		t.Fatalf("再次借不到密钥: %v", err)
	}
	if out := lease.Done(LeaseResult{Verdict: VerdictOK}); !out.Recovered {
		t.Errorf("冷却后的首次成功没有标记为已恢复: %+v", out)
	}

	clock.Advance(time.Minute)
	lease, err = ring.Acquire(nil)
	if err != nil {
		t.Fatalf("成功之后借不到密钥: %v", err)
	}
	if out := lease.Done(LeaseResult{Verdict: VerdictRateLimited}); out.Cooling != time.Second {
		t.Errorf("成功之后退避未重置 = %s, want 1s", out.Cooling)
	}
}

func TestKeyRingPrefersRetryAfter(t *testing.T) {
	clock := newFakeClock()
	ring := newTestRing(t, clock, testCreds(1))

	lease := acquireOK(t, ring)
	out := lease.Done(LeaseResult{
		Verdict: VerdictRateLimited,
		Obs:     Observation{StatusCode: http.StatusTooManyRequests, RetryAfter: 42 * time.Second},
	})

	// 下游说的 42 秒比自己猜的指数更权威：它知道自己什么时候放行。
	if out.Cooling != 42*time.Second {
		t.Errorf("冷却 = %s, want 42s（应当采信 Retry-After）", out.Cooling)
	}
}

// --- 用例 4：401 弃用，且重启后仍是弃用 ---

func TestKeyRingInvalidKeyDisables(t *testing.T) {
	clock := newFakeClock()
	ring := newTestRing(t, clock, testCreds(1))

	lease := acquireOK(t, ring)
	out := lease.Done(LeaseResult{
		Verdict: VerdictInvalidKey,
		Obs:     Observation{StatusCode: http.StatusUnauthorized},
	})
	if !out.Disabled {
		t.Fatalf("401 之后密钥未被弃用: %+v", out)
	}

	if _, err := ring.Acquire(nil); err == nil {
		t.Error("已被弃用的密钥仍能被借出")
	}

	// 重启语义：disabled 是持久态，随输入一起回来。
	restarted := newTestRing(t, clock, []CredentialInput{{
		ID: "key-1", Label: "密钥 1",
		Headers: map[string]string{"Authorization": "Bearer secret-1"},
		Enabled: true, State: KeyDisabled,
	}})
	if _, err := restarted.Acquire(nil); err == nil {
		t.Error("重启后仍在使用一把已知无效的密钥")
	}
}

// --- 用例 5：403 首次不禁用，连续两次才禁用 ---

func TestKeyRingRevokedNeedsTwoStrikes(t *testing.T) {
	clock := newFakeClock()
	ring := newTestRing(t, clock, testCreds(1))

	first := acquireOK(t, ring)
	out1 := first.Done(LeaseResult{
		Verdict: VerdictKeyRevoked,
		Obs:     Observation{StatusCode: http.StatusForbidden},
	})
	if out1.Disabled {
		t.Fatalf("403 第一次就把密钥弃用了：403 也可能是 IP 白名单或 scope 问题，换密钥没用")
	}
	if !strings.Contains(out1.Detail, "403") {
		t.Errorf("未说明这是第二次机会: %q", out1.Detail)
	}

	second := acquireOK(t, ring)
	out2 := second.Done(LeaseResult{
		Verdict: VerdictKeyRevoked,
		Obs:     Observation{StatusCode: http.StatusForbidden},
	})
	if !out2.Disabled {
		t.Errorf("连续两次 403 之后仍未弃用: %+v", out2)
	}
}

// --- 用例 6：全部冷却时快速失败并给出 retryAfter ---

func TestKeyRingAllCoolingFailsFast(t *testing.T) {
	clock := newFakeClock()
	ring := newTestRing(t, clock, testCreds(3))

	// 三把全部被限流。
	for i := 0; i < 3; i++ {
		lease := acquireOK(t, ring)
		lease.Done(LeaseResult{
			Verdict: VerdictRateLimited,
			Obs:     Observation{RetryAfter: 30 * time.Second},
		})
	}

	_, err := ring.Acquire(nil)
	noKey, ok := err.(*ErrNoUsableKey)
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

// --- 用例 7：5xx 与超时让密钥状态零变化 ---

func TestKeyRingNeutralFailureChangesNothing(t *testing.T) {
	clock := newFakeClock()
	ring := newTestRing(t, clock, testCreds(2))

	for _, obs := range []Observation{
		{StatusCode: http.StatusInternalServerError},
		{Err: context.DeadlineExceeded},
	} {
		lease := acquireOK(t, ring)
		id := lease.ID()
		lease.Done(LeaseResult{Verdict: VerdictNeutral, Obs: obs})

		st, ok := ring.StatusOf(id)
		if !ok {
			t.Fatalf("取不到 %s 的状态", id)
		}
		if st.State != KeyActive {
			t.Errorf("5xx/超时之后 %s 的状态变成了 %s：这两个信号与密钥无关", id, st.State)
		}
		if st.ConsecutiveLimited != 0 {
			t.Errorf("%s 的退避计数被 5xx/超时推进了：%d", id, st.ConsecutiveLimited)
		}
	}
}

// --- 用例 8：集体失败保护 ---

func TestKeyRingCollectiveFailureProtection(t *testing.T) {
	clock := newFakeClock()
	ring := newTestRing(t, clock, testCreds(2))

	// 第一把无效：此时还有另一把可用，正常弃用。
	first := acquireOK(t, ring)
	if out := first.Done(LeaseResult{Verdict: VerdictInvalidKey}); !out.Disabled {
		t.Fatalf("在还有备用密钥时没有弃用无效密钥: %+v", out)
	}

	// 第二把也无效：禁用它会让可用密钥归零，且已有同因失败。
	second := acquireOK(t, ring)
	out := second.Done(LeaseResult{Verdict: VerdictInvalidKey})
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

// --- 用例 9：RPM 超额后流量转到下一把 ---

func TestKeyRingRPMExhaustionSpillsOver(t *testing.T) {
	clock := newFakeClock()
	creds := []CredentialInput{
		{ID: "key-1", Label: "A", Headers: map[string]string{"Authorization": "Bearer a"},
			Enabled: true, State: KeyActive, RPMLimit: 2},
		{ID: "key-2", Label: "B", Headers: map[string]string{"Authorization": "Bearer b"},
			Enabled: true, State: KeyActive, RPMLimit: 2},
	}
	ring := newTestRing(t, clock, creds)

	counts := map[string]int{}
	for i := 0; i < 4; i++ {
		lease := acquireOK(t, ring)
		counts[lease.ID()]++
		lease.Done(LeaseResult{Verdict: VerdictOK})
	}
	for id, n := range counts {
		if n != 2 {
			t.Errorf("%s 在 RPM=2 下被用了 %d 次", id, n)
		}
	}

	_, err := ring.Acquire(nil)
	noKey, ok := err.(*ErrNoUsableKey)
	if !ok {
		t.Fatalf("配额用尽后仍借到了密钥: %v", err)
	}
	if noKey.Exhausted != 2 {
		t.Errorf("配额用尽的计数 = %d, want 2: %+v", noKey.Exhausted, noKey)
	}

	// 下个窗口恢复。
	clock.Advance(time.Minute)
	if _, err := ring.Acquire(nil); err != nil {
		t.Errorf("窗口滚动后仍未恢复: %v", err)
	}
}

// --- 用例 10：重启语义 —— cooling 复位、disabled 保持 ---

func TestKeyRingRestartSemantics(t *testing.T) {
	clock := newFakeClock()
	ring := newTestRing(t, clock, testCreds(1))

	lease := acquireOK(t, ring)
	lease.Done(LeaseResult{Verdict: VerdictRateLimited, Obs: Observation{RetryAfter: time.Hour}})
	if _, err := ring.Acquire(nil); err == nil {
		t.Fatal("限流中的密钥仍可借出")
	}

	// 进程重启：库里的键状态还是 active（cooling 从不落库）。
	// 重启往往就是为了从限流里恢复，把限流状态持久化只会让新进程背旧账。
	restarted := newTestRing(t, clock, testCreds(1))
	if _, err := restarted.Acquire(nil); err != nil {
		t.Errorf("重启后仍被限流状态挡住: %v", err)
	}
}

// --- 用例 11：Sync 保留运行态 ---

func TestKeyRingSyncKeepsRuntimeState(t *testing.T) {
	clock := newFakeClock()
	ring := newTestRing(t, clock, testCreds(2))

	lease := acquireOK(t, ring)
	id := lease.ID()
	lease.Done(LeaseResult{Verdict: VerdictRateLimited, Obs: Observation{RetryAfter: time.Hour}})

	// 改一次 label（最常见的"顺手改配置"），冷却不该被清掉：
	// 否则等于给用户开了一个"点一下即可绕过限流"的后门。
	creds := testCreds(2)
	for i := range creds {
		creds[i].Label = "改名 " + creds[i].Label
	}
	ring.Sync(creds)

	st, _ := ring.StatusOf(id)
	if st.State != KeyCooling {
		t.Errorf("Sync 之后 %s 的冷却被清掉了: state=%s", id, st.State)
	}
}

// --- 用例 12：脱敏指纹 ---

func TestKeyFingerprintDoesNotLeakAndDistinguishes(t *testing.T) {
	a := KeyFingerprint(map[string]string{"Authorization": "Bearer alpha-secret"})
	b := KeyFingerprint(map[string]string{"Authorization": "Bearer beta-secret"})
	same := KeyFingerprint(map[string]string{"Authorization": "Bearer alpha-secret"})

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

// --- 用例 13：VerdictOf 映射 ---

func TestVerdictOfMapping(t *testing.T) {
	cases := []struct {
		name string
		kind ErrorKind
		obs  Observation
		want KeyVerdict
	}{
		{"成功", KindNone, Observation{StatusCode: 200}, VerdictOK},
		{"429", KindRateLimited, Observation{StatusCode: 429}, VerdictRateLimited},
		{"402", KindQuotaExhausted, Observation{StatusCode: 402}, VerdictQuotaExhausted},
		{"401", KindAuthRequired, Observation{StatusCode: 401}, VerdictInvalidKey},
		{"403", KindAuthRequired, Observation{StatusCode: 403}, VerdictKeyRevoked},
		{"5xx", KindUpstreamError, Observation{StatusCode: 500}, VerdictNeutral},
		{"超时", KindTimeout, Observation{}, VerdictNeutral},
		{"DNS", KindDNSFail, Observation{}, VerdictNeutral},
		{"业务错误", KindUnknown, Observation{StatusCode: 200}, VerdictNeutral},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := VerdictOf(tc.kind, tc.obs); got != tc.want {
				t.Errorf("VerdictOf(%s) = %s, want %s", tc.kind, got, tc.want)
			}
		})
	}
}

// --- 重试边界的守卫 ---

func TestKeyVerdictRetryBoundary(t *testing.T) {
	// 可重试 = 确定请求未被执行。这四类都满足。
	for _, v := range []KeyVerdict{VerdictRateLimited, VerdictQuotaExhausted, VerdictInvalidKey, VerdictKeyRevoked} {
		if !v.Retryable() {
			t.Errorf("%s 应当允许换密钥重试", v)
		}
	}
	// 中性失败可能已经执行过：重试会让有副作用的工具跑第二遍。
	for _, v := range []KeyVerdict{VerdictNeutral, VerdictOK} {
		if v.Retryable() {
			t.Errorf("%s 不该允许重试：请求可能已经执行", v)
		}
	}
}

// --- 密钥池打空要有自己的分类 ---

// NO_USABLE_KEY 这个枚举值如果没有人产生，用户就会看到"未知错误"：
// 一段本可执行的说明（还有几把在冷却、约多久后重试）被降级成一句
// 无用的"稍后重试即可"。这条守的是分类接线，不是分类定义。
func TestNoUsableKeyHasItsOwnKind(t *testing.T) {
	noKey := &ErrNoUsableKey{
		ServerKey:  "s",
		Total:      3,
		Cooling:    2,
		Disabled:   1,
		RetryAfter: 30 * time.Second,
	}

	if got := CallErrorKind(noKey); got != KindNoUsableKey {
		t.Errorf("CallErrorKind = %s, want NO_USABLE_KEY", got)
	}
	// 上层会把它包一层（比如预算耗尽时挂 Cause），包装后仍要认得出。
	wrapped := fmt.Errorf("调用失败: %w", noKey)
	if got := CallErrorKind(wrapped); got != KindNoUsableKey {
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

// --- 429 的响应体区分限流与额度耗尽 ---

// classify 需要一个非 nil 的错误才会走到 HTTP 分支；而它又必须是
// "没有底层传输错误"的那种（obs.Err == nil 才会采信状态码）。
var errUpstream = errors.New("request failed with status 429")

func TestClassifyDistinguishesQuotaFromRateLimit(t *testing.T) {
	rate := classify(errUpstream, Observation{
		StatusCode: 429, BodySnippet: `{"error":{"message":"rate limit reached"}}`,
	})
	if rate != KindRateLimited {
		t.Errorf("普通 429 分类 = %s, want RATE_LIMITED", rate)
	}

	quota := classify(errUpstream, Observation{
		StatusCode:  429,
		BodySnippet: `{"error":{"message":"You exceeded your current quota","code":"insufficient_quota"}}`,
	})
	if quota != KindQuotaExhausted {
		t.Errorf("额度耗尽分类 = %s, want QUOTA_EXHAUSTED:"+
			"判错会让一把没钱的密钥被无限退避重试", quota)
	}

	payment := classify(errUpstream, Observation{StatusCode: 402})
	if payment != KindQuotaExhausted {
		t.Errorf("402 分类 = %s, want QUOTA_EXHAUSTED", payment)
	}
}

// Retry-After 的两种格式都要认：RFC 7231 允许秒数与 HTTP-date，
// 实际供应商两种都在用。
func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)

	if got := parseRetryAfter("30", now); got != 30*time.Second {
		t.Errorf("秒数格式 = %s, want 30s", got)
	}
	if got := parseRetryAfter(now.Add(90*time.Second).UTC().Format(http.TimeFormat), now); got != 90*time.Second {
		t.Errorf("HTTP-date 格式 = %s, want 90s", got)
	}
	// 解析不出来时返回 0 而不是猜一个值：猜会让"下游说 30s 后好"
	// 被悄悄改成别的数字。
	if got := parseRetryAfter("soon", now); got != 0 {
		t.Errorf("无法解析时 = %s, want 0", got)
	}
	if got := parseRetryAfter("-5", now); got != 0 {
		t.Errorf("负数 = %s, want 0", got)
	}
}
