package tests

import (
	"context"
	"testing"
	"time"

	"github.com/hydra/mcp-hub/internal/store"
	"github.com/hydra/mcp-hub/internal/store/credentials"
	"github.com/hydra/mcp-hub/internal/store/servers"
)

// mustServerWithKey 建一个指定 key 的 server 并返回它。
func mustServerWithKey(t *testing.T, st *store.Store, key string) servers.Server {
	t.Helper()
	spec := testSpec()
	spec.Key = key
	if err := st.Servers.Upsert(context.Background(), servers.Config{Spec: spec, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	sv, err := st.Servers.GetByKey(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	return sv
}

// O4：冷却到点时刻要能落库并读回——重启后恢复内存态全靠这一列。
func TestCredentialCoolingUntilRoundTrip(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	sv := mustServerWithKey(t, st, "cool")
	c, err := st.Credentials.Insert(ctx, credentials.Credential{
		ServerID: sv.Spec.ID,
		Label:    "k1",
		Headers:  map[string]string{"Authorization": "Bearer a"},
		Enabled:  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !c.CoolingUntil.IsZero() {
		t.Fatalf("新密钥不该带冷却: %v", c.CoolingUntil)
	}

	until := time.Now().Add(30 * time.Second).Truncate(time.Millisecond)
	if err := st.Credentials.SetCoolingUntil(ctx, c.ID, until); err != nil {
		t.Fatal(err)
	}
	got, err := st.Credentials.Get(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.CoolingUntil.Equal(until) {
		t.Fatalf("冷却到点 = %v, want %v", got.CoolingUntil, until)
	}

	// 恢复（或冷却结束）后写零值即清掉。
	if err := st.Credentials.SetCoolingUntil(ctx, c.ID, time.Time{}); err != nil {
		t.Fatal(err)
	}
	got, err = st.Credentials.Get(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.CoolingUntil.IsZero() {
		t.Fatalf("清零后仍读到冷却到点 %v", got.CoolingUntil)
	}
}

// 人工救回（M8/M10）必须连冷却一起清：留着旧冷却等于"救回来但还在关禁闭"，
// 重启后更是会把刚救回的密钥重新按进冷却。
func TestReEnableClearsCoolingUntil(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	sv := mustServerWithKey(t, st, "cool2")
	c, err := st.Credentials.Insert(ctx, credentials.Credential{
		ServerID: sv.Spec.ID,
		Label:    "k1",
		Headers:  map[string]string{"Authorization": "Bearer a"},
		Enabled:  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Credentials.SetCoolingUntil(ctx, c.ID, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := st.Credentials.Disable(ctx, c.ID, "QUOTA_EXHAUSTED", "额度用尽"); err != nil {
		t.Fatal(err)
	}
	if err := st.Credentials.SetEnabled(ctx, c.ID, true); err != nil {
		t.Fatal(err)
	}
	got, err := st.Credentials.Get(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.CoolingUntil.IsZero() {
		t.Fatalf("重新启用后仍残留冷却到点 %v", got.CoolingUntil)
	}
	if got.State != credentials.CredStateActive {
		t.Fatalf("状态 = %s, want active", got.State)
	}
}

// 密钥池装载走 ListAll：冷却到点必须出现在那一份快照里，
// 否则"落库了但恢复不了"。
func TestCoolingUntilVisibleToRingLoader(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	sv := mustServerWithKey(t, st, "cool3")
	c, err := st.Credentials.Insert(ctx, credentials.Credential{
		ServerID: sv.Spec.ID,
		Label:    "k1",
		Headers:  map[string]string{"Authorization": "Bearer a"},
		Enabled:  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	until := time.Now().Add(2 * time.Minute).Truncate(time.Millisecond)
	if err := st.Credentials.SetCoolingUntil(ctx, c.ID, until); err != nil {
		t.Fatal(err)
	}

	all, err := st.Credentials.ListAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	list := all[sv.Spec.ID]
	if len(list) != 1 {
		t.Fatalf("ListAll 返回 %d 把, want 1", len(list))
	}
	if !list[0].CoolingUntil.Equal(until) {
		t.Fatalf("ListAll 里的冷却到点 = %v, want %v", list[0].CoolingUntil, until)
	}
}
