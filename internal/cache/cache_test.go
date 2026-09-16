package cache_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hydra/mcp-hub/internal/cache"
	"github.com/hydra/mcp-hub/internal/cache/cachetest"
)

func TestTypedRoundTripAndInvalidate(t *testing.T) {
	m := cachetest.New()
	ctx := context.Background()

	type payload struct{ N int }
	c := cache.NewTyped[payload](m, "t")

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
	if _, ok := cache.NewTyped[payload](m, "other").Get(ctx, "k"); ok {
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
	m := cachetest.New()
	ctx := context.Background()
	c := cache.NewTyped[int](m, "t")

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
	m := cachetest.New()
	ctx := context.Background()
	c := cache.NewTyped[int](m, "t")

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
