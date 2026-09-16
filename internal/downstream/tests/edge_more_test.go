package tests

import (
	"sync"
	"testing"
	"time"

	"github.com/hydra/mcp-hub/internal/downstream"
)

/*
这一组覆盖的是"上一次实测没摸到、单元测试也没钉住"的那批边界：
退避预算怎么花完（R2）、未计量时配额窗口该放行还是该拒（R8）、
窗口翻转瞬间的并发（R12）、重启后退避进度归零（R15 前半）。

它们都靠 fakeClock 推进时间：真去睡够退避的秒数，用例就会因为慢而被
跳过——而被跳过的恰好是最容易写错的退避逻辑。
*/

// --- R2：换了一把，新的也 429 ------------------------------------------------

// 预算 = 可用密钥数。花完之后必须立刻失败并给出"多久后再试"，
// 而且同一轮里已经失败过的钥匙绝不再碰——再打只会加重限流。
func TestKeyRingExhaustsBudgetThenFailsFast(t *testing.T) {
	clock := newFakeClock()
	ring := newTestRing(t, clock, testCreds(3))

	exclude := map[string]bool{}
	used := map[string]int{}

	for round := 0; round < 3; round++ {
		lease, err := ring.Acquire(exclude)
		if err != nil {
			t.Fatalf("第 %d 轮就借不到密钥了（预算应=3）: %v", round+1, err)
		}
		if exclude[lease.ID()] {
			t.Fatalf("第 %d 轮借到了本轮已失败的密钥 %s", round+1, lease.ID())
		}
		used[lease.ID()]++
		out := lease.Done(downstream.LeaseResult{
			Verdict: downstream.VerdictRateLimited,
			Obs:     downstream.Observation{RetryAfter: 30 * time.Second},
		})
		if out.Cooling != 30*time.Second {
			t.Errorf("第 %d 轮冷却 = %s, want 30s（下游说了就算）", round+1, out.Cooling)
		}
		exclude[lease.ID()] = true
	}
	if len(used) != 3 {
		t.Errorf("三轮里用过的密钥 = %d, want 3: %v", len(used), used)
	}

	// 预算花完：本轮三把都试过了，Acquire 到此为止。
	if _, err := ring.Acquire(exclude); err == nil {
		t.Fatal("预算花完仍借到了密钥")
	}

	// 但对外报的那个错误来自不带 exclude 的复探：带 exclude 探测会把刚失败的
	// 三把全部跳过，冷却计数变成 0——而"几把在冷却、多久能回来"恰恰是调用方
	// 最想知道的。pool/call.go 也是这么做的。
	noKey := ring.Unavailable(nil)
	if noKey == nil {
		t.Fatal("预算花完却仍报告有可用密钥")
	}
	if noKey.Cooling != 3 {
		t.Errorf("冷却计数 = %d, want 3: %+v", noKey.Cooling, noKey)
	}
	if noKey.RetryAfter <= 0 {
		t.Errorf("没给出恢复时间，调用方无从判断该等多久: %+v", noKey)
	}
	if noKey.RetryAfter > 30*time.Second {
		t.Errorf("恢复时间 = %s, 应取最近的那个冷却到点（≤30s）", noKey.RetryAfter)
	}
}

// --- R12：配额窗口翻转瞬间的并发 ---------------------------------------------

// 窗口计数在锁内完成，并发抢不能超卖：超卖等于把限流压力推回下游，
// 而下游的回击（429 甚至封禁）会落到所有密钥头上。
func TestKeyRingRPMWindowDoesNotOversellUnderConcurrency(t *testing.T) {
	clock := newFakeClock()
	creds := []downstream.CredentialInput{{
		ID: "key-1", Label: "A",
		Headers: map[string]string{"Authorization": "Bearer a"},
		Enabled: true, State: downstream.KeyActive, RPMLimit: 5,
	}}
	ring := newTestRing(t, clock, creds)

	const goroutines = 64
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		granted int
		ids     = map[string]int{}
	)
	start := make(chan struct{})
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			lease, err := ring.Acquire(nil)
			mu.Lock()
			if err == nil {
				granted++
				ids[lease.ID()]++
				lease.Done(downstream.LeaseResult{Verdict: downstream.VerdictOK})
			}
			mu.Unlock()
		}()
	}
	close(start)
	wg.Wait()

	if granted != 5 {
		t.Fatalf("RPM=5 下并发发出 %d 个租约, want 恰好 5", granted)
	}

	// 窗口翻转后恢复。
	clock.Advance(time.Minute)
	if _, err := ring.Acquire(nil); err != nil {
		t.Errorf("窗口翻转后仍未恢复: %v", err)
	}
}

// --- R15 前半：重启后冷却在、退避进度不在 --------------------------------------

// 冷却跨重启恢复（O4 已测）；这里钉住另一半：连续限流次数不落库，
// 重启后从头开始退避。可接受——最坏是多走一轮 1s→2s。
func TestKeyRingRestartKeepsCoolingButResetsBackoffProgress(t *testing.T) {
	clock := newFakeClock()
	ring := newTestRing(t, clock, testCreds(1))

	// 连续三次限流：1s → 2s → 4s。
	for i, want := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second} {
		lease := acquireOK(t, ring)
		if out := lease.Done(downstream.LeaseResult{Verdict: downstream.VerdictRateLimited}); out.Cooling != want {
			t.Fatalf("第 %d 次退避 = %s, want %s", i+1, out.Cooling, want)
		}
		clock.Advance(want + time.Millisecond)
	}

	before, _ := ring.StatusOf("key-1")
	if before.ConsecutiveLimited != 3 {
		t.Fatalf("重启前连续限流次数 = %d, want 3", before.ConsecutiveLimited)
	}

	// 重启：新进程按库里的 cooling_until 恢复冷却，但计数随内存一起没了。
	restarted := newTestRing(t, clock, []downstream.CredentialInput{
		{ID: "key-1", Label: "密钥 1",
			Headers:      map[string]string{"Authorization": "Bearer secret-1"},
			Enabled:      true,
			State:        downstream.KeyActive,
			CoolingUntil: before.CoolingUntil,
		},
	})
	after, _ := restarted.StatusOf("key-1")
	if after.ConsecutiveLimited != 0 {
		t.Errorf("重启后连续限流次数 = %d, want 0（进度不落库）", after.ConsecutiveLimited)
	}
	if !after.CoolingUntil.Equal(before.CoolingUntil) {
		t.Errorf("重启后冷却到点 = %v, want %v", after.CoolingUntil, before.CoolingUntil)
	}

	// 冷却到期后第一次撞限流：从 1s 重来。
	clock.Advance(time.Minute)
	lease := acquireOK(t, restarted)
	if out := lease.Done(downstream.LeaseResult{Verdict: downstream.VerdictRateLimited}); out.Cooling != time.Second {
		t.Errorf("重启后第一次退避 = %s, want 1s（进度归零）", out.Cooling)
	}
}
