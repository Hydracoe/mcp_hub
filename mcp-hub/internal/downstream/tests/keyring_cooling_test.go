package tests

import (
	"testing"
	"time"

	"github.com/hydra/mcp-hub/internal/downstream"
)

// --- O4：重启后从库里恢复冷却 ---

// 新起的进程拿到一把"库里还在冷却中"的密钥，必须当场就按冷却处理：
// 这正是 O4 要修的场景——重启往往就是为了从限流里恢复，若重启即忘，
// 新进程会对仍在下游限流窗口内的密钥重打一轮，把 429 招来第二遍。
func TestKeyRingRestoresCoolingFromStorage(t *testing.T) {
	clock := newFakeClock()
	until := clock.Now().Add(30 * time.Second)

	creds := []downstream.CredentialInput{{
		ID:           "key-1",
		Label:        "密钥 1",
		Headers:      map[string]string{"Authorization": "Bearer secret-1"},
		Enabled:      true,
		State:        downstream.KeyActive,
		CoolingUntil: until,
	}}
	ring := newTestRing(t, clock, creds)

	if _, err := ring.Acquire(nil); err == nil {
		t.Fatal("库里在冷却的密钥被借了出去：冷却没有恢复")
	}
	unavail := ring.Unavailable(nil)
	if unavail == nil || unavail.Cooling != 1 {
		t.Fatalf("不可用原因 = %+v, want 1 把冷却中", unavail)
	}
	if unavail.RetryAfter <= 0 {
		t.Fatal("恢复的冷却没带出\"多久后重试\"：调用方拿不到可执行的指引")
	}
	if unavail.RetryAfter > 30*time.Second {
		t.Errorf("RetryAfter = %s, 不该超过落库的冷却剩余 %s", unavail.RetryAfter, 30*time.Second)
	}

	// 到点后自动回来，且第一次成功要报 Recovered——
	// "冷却到期"与"真的恢复了"是两件事，AI/运维看到的应该是后者。
	clock.Advance(31 * time.Second)
	lease := acquireOK(t, ring)
	out := lease.Done(downstream.LeaseResult{Verdict: downstream.VerdictOK})
	if !out.Recovered {
		t.Error("冷却恢复后的首次成功未标记 Recovered")
	}
}

// 库里的冷却时刻已经过去，就是过期数据：按"没在冷却"处理，
// 绝不能拿一个过去的时间戳把密钥按在原地。
func TestKeyRingIgnoresExpiredCooling(t *testing.T) {
	clock := newFakeClock()
	creds := []downstream.CredentialInput{{
		ID:           "key-1",
		Label:        "密钥 1",
		Headers:      map[string]string{"Authorization": "Bearer secret-1"},
		Enabled:      true,
		State:        downstream.KeyActive,
		CoolingUntil: clock.Now().Add(-time.Minute),
	}}
	ring := newTestRing(t, clock, creds)

	lease := acquireOK(t, ring)
	if out := lease.Done(downstream.LeaseResult{Verdict: downstream.VerdictOK}); out.Recovered {
		t.Error("过期的冷却不该被当成\"刚恢复\"")
	}
}

// 恢复只发生在 entry 首次创建时：之后每次 Sync 都带着此刻真实的运行态，
// 用库里的旧值覆盖它，等于"改一次备注就能把冷却改回去"（M7 明确禁止）。
func TestKeyRingSyncDoesNotResurrectStoredCooling(t *testing.T) {
	clock := newFakeClock()
	creds := []downstream.CredentialInput{{
		ID:           "key-1",
		Label:        "密钥 1",
		Headers:      map[string]string{"Authorization": "Bearer secret-1"},
		Enabled:      true,
		State:        downstream.KeyActive,
		CoolingUntil: clock.Now().Add(30 * time.Second),
	}}
	ring := newTestRing(t, clock, creds)

	// 冷却自然到期（推进时间），此时池子里它已可用。
	clock.Advance(31 * time.Second)

	// 再灌一次持久快照（改备注触发的 Sync 就是这条路径），
	// 库里那个到点时刻还没被清掉——不能因此又把它按回冷却。
	ring.Sync(creds)
	if _, err := ring.Acquire(nil); err != nil {
		t.Fatalf("Sync 之后密钥不可用了：库里的旧冷却被复活了 (%v)", err)
	}
}
