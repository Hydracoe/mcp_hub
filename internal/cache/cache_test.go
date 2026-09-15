package cache

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeClock 是可推进的时钟，用来免去 sleep。
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{t: time.Unix(1700000000, 0)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newTestMemory(t *testing.T, opts ...Option) (*Memory, *fakeClock) {
	t.Helper()
	clock := newFakeClock()
	m := NewMemory(append([]Option{WithClock(clock.Now), WithSweepInterval(0)}, opts...)...)
	t.Cleanup(func() { _ = m.Close() })
	return m, clock
}

func TestMemorySetGetDelete(t *testing.T) {
	m, _ := newTestMemory(t)
	ctx := context.Background()

	if _, ok, _ := m.Get(ctx, "nope"); ok {
		t.Fatal("不存在的 key 不该命中")
	}

	if err := m.Set(ctx, "a", []byte("1"), 0); err != nil {
		t.Fatalf("Set 失败: %v", err)
	}
	if v, ok, _ := m.Get(ctx, "a"); !ok || string(v) != "1" {
		t.Fatalf("Get = %q, %v", v, ok)
	}

	if err := m.Delete(ctx, "a"); err != nil {
		t.Fatalf("Delete 失败: %v", err)
	}
	if _, ok, _ := m.Get(ctx, "a"); ok {
		t.Fatal("删除后仍命中")
	}
}

// 缓存里的字节被调用方改写，不该污染缓存本身。
func TestMemoryReturnsCopy(t *testing.T) {
	m, _ := newTestMemory(t)
	ctx := context.Background()

	_ = m.Set(ctx, "a", []byte("orig"), 0)
	got, _, _ := m.Get(ctx, "a")
	got[0] = 'X'

	again, _, _ := m.Get(ctx, "a")
	if string(again) != "orig" {
		t.Errorf("缓存内容被外部改写: %q", again)
	}
}

func TestMemoryTTL(t *testing.T) {
	m, clock := newTestMemory(t)
	ctx := context.Background()

	_ = m.Set(ctx, "ttl", []byte("v"), 5*time.Second)
	_ = m.Set(ctx, "forever", []byte("v"), 0)

	clock.advance(4 * time.Second)
	if _, ok, _ := m.Get(ctx, "ttl"); !ok {
		t.Error("未到期的 key 不该失效")
	}

	clock.advance(2 * time.Second)
	if _, ok, _ := m.Get(ctx, "ttl"); ok {
		t.Error("过期的 key 仍命中")
	}
	if _, ok, _ := m.Get(ctx, "forever"); !ok {
		t.Error("ttl=0 的 key 不该过期")
	}
}

func TestMemoryDeletePrefix(t *testing.T) {
	m, _ := newTestMemory(t)
	ctx := context.Background()

	for _, k := range []string{"hub:servers:all", "hub:servers:1", "hub:stats:24h"} {
		_ = m.Set(ctx, k, []byte("v"), 0)
	}

	if err := m.DeletePrefix(ctx, "hub:servers:"); err != nil {
		t.Fatalf("DeletePrefix 失败: %v", err)
	}
	for _, k := range []string{"hub:servers:all", "hub:servers:1"} {
		if _, ok, _ := m.Get(ctx, k); ok {
			t.Errorf("%s 应被前缀删除", k)
		}
	}
	if _, ok, _ := m.Get(ctx, "hub:stats:24h"); !ok {
		t.Error("前缀之外的 key 不该被删")
	}
}

// 前缀必须按整段匹配，不能把 hub:servers 当成 hub:server 的扩展。
func TestMemoryDeletePrefixIsNotStringPrefix(t *testing.T) {
	m, _ := newTestMemory(t)
	ctx := context.Background()

	_ = m.Set(ctx, "hub:servers:all", []byte("v"), 0)
	_ = m.Set(ctx, "hub:serversX", []byte("v"), 0)

	if err := m.DeletePrefix(ctx, "hub:servers:"); err != nil {
		t.Fatalf("DeletePrefix 失败: %v", err)
	}
	if _, ok, _ := m.Get(ctx, "hub:serversX"); !ok {
		t.Error("不该把 hub:serversX 一起删掉")
	}
}

func TestMemoryEviction(t *testing.T) {
	m, _ := newTestMemory(t, WithMaxItems(3))
	ctx := context.Background()

	for i, k := range []string{"a", "b", "c", "d", "e"} {
		_ = m.Set(ctx, k, []byte{byte(i)}, time.Duration(i+1)*time.Second)
	}
	if got := m.Stats().Entries; got > 3 {
		t.Fatalf("条目数 = %d, 上限 3", got)
	}
	if m.Stats().Evictions == 0 {
		t.Error("应该有淘汰发生")
	}
}

func TestMemoryCloseIsIdempotent(t *testing.T) {
	m := NewMemory()
	if err := m.Close(); err != nil {
		t.Fatalf("Close 失败: %v", err)
	}
	if err := m.Close(); err != nil {
		t.Fatalf("二次 Close 失败: %v", err)
	}
}

func TestTypedRoundTripAndInvalidate(t *testing.T) {
	m, _ := newTestMemory(t)
	ctx := context.Background()

	type payload struct{ N int }
	c := NewTyped[payload](m, "t")

	if _, ok := c.Get(ctx, "k"); ok {
		t.Fatal("空缓存不该命中")
	}
	if err := c.Set(ctx, "k", payload{N: 7}, time.Minute); err != nil {
		t.Fatalf("Set 失败: %v", err)
	}
	if v, ok := c.Get(ctx, "k"); !ok || v.N != 7 {
		t.Fatalf("Get = %+v, %v", v, ok)
	}

	// 不同前缀互不干扰。
	if _, ok := NewTyped[payload](m, "other").Get(ctx, "k"); ok {
		t.Error("前缀没隔离")
	}

	if err := c.InvalidateAll(ctx); err != nil {
		t.Fatalf("InvalidateAll 失败: %v", err)
	}
	if _, ok := c.Get(ctx, "k"); ok {
		t.Error("失效后仍命中")
	}
}

// 回源失败不能被写进缓存：一次瞬时故障不该固化成 TTL 那么长的持续故障。
func TestTypedGetOrLoadDoesNotCacheError(t *testing.T) {
	m, _ := newTestMemory(t)
	ctx := context.Background()
	c := NewTyped[int](m, "t")

	var calls atomic.Int64
	load := func(context.Context) (int, error) {
		calls.Add(1)
		return 0, errors.New("下游挂了")
	}

	for i := 0; i < 3; i++ {
		if _, err := c.GetOrLoad(ctx, "k", time.Minute, load); err == nil {
			t.Fatal("期望回源报错")
		}
	}
	if calls.Load() != 3 {
		t.Errorf("回源次数 = %d, 期望 3（错误不该被缓存）", calls.Load())
	}
}

// 同一个 key 的并发回源只能放一个进去，否则冷 key 会放大下游压力。
func TestTypedGetOrLoadSingleFlight(t *testing.T) {
	m, _ := newTestMemory(t)
	ctx := context.Background()
	c := NewTyped[int](m, "t")

	var (
		calls   atomic.Int64
		release = make(chan struct{})
		wg      sync.WaitGroup
	)
	load := func(context.Context) (int, error) {
		calls.Add(1)
		<-release
		return 42, nil
	}

	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if v, err := c.GetOrLoad(ctx, "k", time.Minute, load); err != nil || v != 42 {
				t.Errorf("GetOrLoad = %d, %v", v, err)
			}
		}()
	}

	// 等第一个进入回源，再放行，其余请求应当直接命中缓存。
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()

	if n := calls.Load(); n != 1 {
		t.Errorf("回源次数 = %d, 期望 1", n)
	}
}
