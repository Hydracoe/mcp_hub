package tests

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/hydra/mcp-hub/internal/cache"
	"github.com/hydra/mcp-hub/internal/cache/cachetest"
	"github.com/hydra/mcp-hub/internal/downstream"
	"github.com/hydra/mcp-hub/internal/hub"
	"github.com/hydra/mcp-hub/internal/store"
	"github.com/hydra/mcp-hub/internal/store/servers"
	"github.com/hydra/mcp-hub/internal/store/storetest"
)

// spyStore 包一层真实缓存并记账，用来断言"读走没走缓存、写有没有删缓存"。
//
// 光看接口返回是对不出这个结论的：缓存命中与缓存未命中返回的东西
// 一模一样，只有计数能区分。
type spyStore struct {
	inner cache.Store

	mu       sync.Mutex
	gets     int
	hits     int
	sets     int
	prefixes []string
}

func (s *spyStore) Get(ctx context.Context, key string) ([]byte, bool, error) {
	raw, ok, err := s.inner.Get(ctx, key)
	s.mu.Lock()
	s.gets++
	if ok {
		s.hits++
	}
	s.mu.Unlock()
	return raw, ok, err
}

func (s *spyStore) Set(ctx context.Context, key string, v []byte, ttl time.Duration) error {
	s.mu.Lock()
	s.sets++
	s.mu.Unlock()
	return s.inner.Set(ctx, key, v, ttl)
}

func (s *spyStore) Delete(ctx context.Context, keys ...string) error {
	return s.inner.Delete(ctx, keys...)
}

func (s *spyStore) DeletePrefix(ctx context.Context, prefix string) error {
	s.mu.Lock()
	s.prefixes = append(s.prefixes, prefix)
	s.mu.Unlock()
	return s.inner.DeletePrefix(ctx, prefix)
}

func (s *spyStore) Close() error { return s.inner.Close() }

func (s *spyStore) snapshot() (gets, hits, sets, prefixes int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gets, s.hits, s.sets, len(s.prefixes)
}

func newCachedEnv(t *testing.T) (*hub.Service, *store.Store, *spyStore) {
	t.Helper()

	st, err := store.Open(storetest.DSN(t))
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	spy := &spyStore{inner: cachetest.New()}
	svc := hub.NewServiceWithCache(st, nil, spy)
	t.Cleanup(func() { svc.Close() })

	return svc, st, spy
}

func testSpec(key string) downstream.Spec {
	return downstream.Spec{
		Key:       key,
		Name:      key,
		Transport: downstream.TransportStreamableHTTP,
		URL:       "http://127.0.0.1:1/mcp",
		Timeout:   time.Second,
	}
}

// 第二次读必须命中缓存，不能再回源。
func TestServersReadGoesThroughCache(t *testing.T) {
	svc, _, spy := newCachedEnv(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if _, err := svc.Servers(ctx); err != nil {
			t.Fatalf("Servers 失败: %v", err)
		}
	}

	gets, hits, sets, _ := spy.snapshot()
	if sets != 1 {
		t.Errorf("回源写入次数 = %d, 期望 1（后两次该命中缓存）", sets)
	}
	// 3 次调用里后 2 次命中；第一次未命中时 GetOrLoad 会做一次二次检查
	// （防止等锁期间别人已经回源），所以是 4 次 Get 而不是 3 次。
	if hits != 2 || gets != 4 {
		t.Errorf("gets/hits = %d/%d, 期望 4/2", gets, hits)
	}
}

// 写路径必须立刻把列表缓存清掉，否则用户看到的是"改了没生效"。
func TestServersCacheInvalidatedOnWrite(t *testing.T) {
	svc, st, spy := newCachedEnv(t)
	ctx := context.Background()

	// 先把缓存填上，这样后面读到的若是旧值，就只可能是缓存没清。
	if list, err := svc.Servers(ctx); err != nil || len(list) != 0 {
		t.Fatalf("初始列表 = %+v, err = %v", list, err)
	}
	_, _, setsBefore, prefixesBefore := spy.snapshot()

	cat, err := st.Categories.Create(ctx, "默认", "default", "")
	if err != nil {
		t.Fatalf("建分类失败: %v", err)
	}
	if _, err := svc.ApplyServer(ctx, servers.Config{Spec: testSpec("alpha"), CategoryID: cat.ID, Enabled: true}); err != nil {
		t.Fatalf("ApplyServer 失败: %v", err)
	}

	list, err := svc.Servers(ctx)
	if err != nil {
		t.Fatalf("Servers 失败: %v", err)
	}
	if len(list) != 1 || list[0].Spec.Key != "alpha" {
		t.Fatalf("保存后列表 = %+v, 期望含 alpha", list)
	}

	_, _, setsAfter, prefixesAfter := spy.snapshot()
	if prefixesAfter == prefixesBefore {
		t.Error("写路径没有触发缓存失效")
	}
	if setsAfter == setsBefore {
		t.Error("失效后应重新回源一次")
	}

	// 启停同样要失效。
	if err := svc.SetEnabled(ctx, list[0].Spec.ID, false); err != nil {
		t.Fatalf("SetEnabled 失败: %v", err)
	}
	_, _, _, prefixesAfterDisable := spy.snapshot()
	if prefixesAfterDisable == prefixesAfter {
		t.Error("SetEnabled 没有触发缓存失效")
	}

	list, err = svc.Servers(ctx)
	if err != nil {
		t.Fatalf("Servers 失败: %v", err)
	}
	if len(list) != 1 || list[0].Enabled {
		t.Fatalf("停用后列表 = %+v, 期望 enabled=false", list)
	}
}

// 缓存只清自己的前缀，不能顺手把别人的缓存也清了。
func TestCacheInvalidationScopedToPrefix(t *testing.T) {
	svc, st, spy := newCachedEnv(t)
	ctx := context.Background()

	if _, err := svc.Servers(ctx); err != nil {
		t.Fatalf("Servers 失败: %v", err)
	}
	if _, err := svc.RecentStats(ctx, time.Hour); err != nil {
		t.Fatalf("RecentStats 失败: %v", err)
	}

	cat, err := st.Categories.Create(ctx, "默认", "default", "")
	if err != nil {
		t.Fatalf("建分类失败: %v", err)
	}
	if _, err := svc.ApplyServer(ctx, servers.Config{Spec: testSpec("beta"), CategoryID: cat.ID, Enabled: true}); err != nil {
		t.Fatalf("ApplyServer 失败: %v", err)
	}

	spy.mu.Lock()
	prefixes := append([]string(nil), spy.prefixes...)
	spy.mu.Unlock()

	if len(prefixes) == 0 {
		t.Fatal("没有发生缓存失效")
	}
	for _, p := range prefixes {
		if p != "hub:servers:" {
			t.Errorf("失效了不该动的前缀: %q", p)
		}
	}
}

func TestRecentStatsCached(t *testing.T) {
	svc, _, spy := newCachedEnv(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if _, err := svc.RecentStats(ctx, time.Hour); err != nil {
			t.Fatalf("RecentStats 失败: %v", err)
		}
	}

	if _, _, sets, _ := spy.snapshot(); sets != 1 {
		t.Errorf("统计回源次数 = %d, 期望 1", sets)
	}
}
