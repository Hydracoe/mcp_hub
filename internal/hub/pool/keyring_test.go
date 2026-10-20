package pool

import (
	"testing"

	"github.com/hydra/mcp-hub/internal/downstream"
)

// testCreds 造若干把可用密钥。
func testCreds(ids ...string) []downstream.CredentialInput {
	out := make([]downstream.CredentialInput, 0, len(ids))
	for _, id := range ids {
		out = append(out, downstream.CredentialInput{
			ID:      id,
			Label:   id,
			Headers: map[string]string{"Authorization": "Bearer " + id},
			Enabled: true,
			State:   downstream.KeyActive,
		})
	}
	return out
}

// testRingOpts 是测试用的密钥池参数：关掉抖动，让断言是确定的。
func testRingOpts(p *Pool, spec downstream.Spec) downstream.KeyRingOptions {
	return downstream.KeyRingOptions{
		ServerID:    spec.ID,
		ServerKey:   spec.Key,
		JitterRatio: -1,
		Now:         p.now,
	}
}

// 停用的 server 不该继续持有密钥条目。
//
// Sync 是池子与最新配置对齐的那一步：消失的 server 的密钥池在这里被清掉。
// 停用的 server 同理——它的密钥还在库里（重新启用时会装回来），但留在池里
// 就意味着随时可能被选中发出去。停用必须真的停用，而不是只把工具从
// 聚合列表里摘掉。
func TestSyncDropsKeyRingOfDisabledServer(t *testing.T) {
	p := newTestPool(t)

	on := downstream.Spec{ID: "srv-on", Key: "on"}
	off := downstream.Spec{ID: "srv-off", Key: "off"}

	p.SyncKeyRing(on.ID, testRingOpts(p, on), testCreds("k-on"))
	p.SyncKeyRing(off.ID, testRingOpts(p, off), testCreds("k-off"))

	p.Sync([]Target{
		{Spec: on, Enabled: true},
		{Spec: off, Enabled: false},
	})

	if p.KeyRing(off.ID) != nil {
		t.Error("停用的 server 的密钥池仍留在池中：那些密钥随时可能被选中发出去")
	}
	if st := p.KeyStatusesOf(off.ID); len(st) != 0 {
		t.Errorf("停用的 server 仍报得出密钥状态: %+v", st)
	}
	// 运行中的 server 不受牵连：它的密钥池是它自己的事。
	if p.KeyRing(on.ID) == nil {
		t.Error("启用中的 server 的密钥池被误清了")
	}
}

// 停用 → 重新启用：密钥池能装回来。
//
// 停用清掉的是内存里的条目，不是库里的凭据：密钥还在，重新启用时必须
// 还能用（否则"停用一会儿再开"就等于把密钥全丢了）。
func TestSyncKeyRingRestoredAfterReEnable(t *testing.T) {
	p := newTestPool(t)
	spec := downstream.Spec{ID: "srv-toggle", Key: "toggle"}

	p.SyncKeyRing(spec.ID, testRingOpts(p, spec), testCreds("k1"))
	p.Sync([]Target{{Spec: spec, Enabled: false}})
	if p.KeyRing(spec.ID) != nil {
		t.Fatal("停用后密钥池没有从池中清掉")
	}

	// 重新启用：密钥池装回，密钥重新可用。
	p.Sync([]Target{{Spec: spec, Enabled: true}})
	p.SyncKeyRing(spec.ID, testRingOpts(p, spec), testCreds("k1"))

	ring := p.KeyRing(spec.ID)
	if ring == nil {
		t.Fatal("重新启用后密钥池没有装回来")
	}
	if n := ring.UsableCount(); n != 1 {
		t.Errorf("重新启用后可用密钥数 = %d, want 1", n)
	}
}

// 消失的 server 的密钥池照旧被清掉。
//
// 这是 Sync 一直在做的事：密钥明文不该留在内存里。停用只是把判据从
// "消失了"扩到"不再服务了"。
func TestSyncDropsKeyRingOfRemovedServer(t *testing.T) {
	p := newTestPool(t)
	spec := downstream.Spec{ID: "srv-gone", Key: "gone"}

	p.SyncKeyRing(spec.ID, testRingOpts(p, spec), testCreds("k1"))
	p.Sync(nil)

	if p.KeyRing(spec.ID) != nil {
		t.Error("已删除的 server 的密钥池仍留在池中")
	}
}
