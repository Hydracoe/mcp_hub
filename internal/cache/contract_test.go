package cache_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hydra/mcp-hub/internal/cache"
	"github.com/hydra/mcp-hub/internal/cache/cachetest"
)

// 契约测试：接口的所有实现必须满足同一套语义。
//
// 新加一个后端（今天：进程内内存缓存）时，只要把它挂进下面的 cases，
// "过期、前缀删除、并发安全"这些约定就连带验证了——不必为每个实现
// 重写一遍，也不会出现"换后端之后某条约定悄悄失效"。
func TestStoreContract(t *testing.T) {
	impls := []struct {
		name string
		open func(t *testing.T) cache.Store
	}{
		{"cachetest", func(t *testing.T) cache.Store { return cachetest.New() }},
		{"memory", func(t *testing.T) cache.Store {
			s := cache.NewMemory(cache.MemoryOptions{})
			t.Cleanup(func() { _ = s.Close() })
			return s
		}},
	}

	for _, impl := range impls {
		t.Run(impl.name, func(t *testing.T) {
			t.Run("读写往返与未命中", func(t *testing.T) { testRoundTrip(t, impl.open(t)) })
			t.Run("ttl 过期", func(t *testing.T) { testExpiry(t, impl.open(t)) })
			t.Run("ttl<=0 不过期", func(t *testing.T) { testNoExpiry(t, impl.open(t)) })
			t.Run("前缀删除", func(t *testing.T) { testDeletePrefix(t, impl.open(t)) })
			t.Run("返回值是副本", func(t *testing.T) { testValueIsCopy(t, impl.open(t)) })
			t.Run("并发安全", func(t *testing.T) { testConcurrent(t, impl.open(t)) })
		})
	}
}

func testRoundTrip(t *testing.T, s cache.Store) {
	t.Helper()
	ctx := context.Background()

	if _, ok, err := s.Get(ctx, "missing"); ok || err != nil {
		t.Fatalf("未命中应返回 (nil, false, nil)，得到 (%v, %v, %v)", nil, ok, err)
	}
	if err := s.Set(ctx, "k", []byte("v"), time.Minute); err != nil {
		t.Fatalf("Set 失败: %v", err)
	}
	got, ok, err := s.Get(ctx, "k")
	if err != nil || !ok || string(got) != "v" {
		t.Fatalf("Get = %q, %v, %v", got, ok, err)
	}
	// 删不存在的 key 不算错误。
	if err := s.Delete(ctx, "nope", "k"); err != nil {
		t.Fatalf("Delete 失败: %v", err)
	}
	if _, ok, _ := s.Get(ctx, "k"); ok {
		t.Fatal("删除后仍命中")
	}
}

func testExpiry(t *testing.T, s cache.Store) {
	t.Helper()
	ctx := context.Background()

	if err := s.Set(ctx, "k", []byte("v"), 30*time.Millisecond); err != nil {
		t.Fatalf("Set 失败: %v", err)
	}
	if _, ok, _ := s.Get(ctx, "k"); !ok {
		t.Fatal("过期前应命中")
	}
	time.Sleep(80 * time.Millisecond)
	if _, ok, _ := s.Get(ctx, "k"); ok {
		t.Fatal("过期后不该命中")
	}
}

func testNoExpiry(t *testing.T, s cache.Store) {
	t.Helper()
	ctx := context.Background()

	// ttl <= 0 表示不设过期；负数也要被当成"不过期"而不是"立刻过期"。
	for _, ttl := range []time.Duration{0, -time.Minute} {
		key := "k"
		if ttl < 0 {
			key = "k-neg"
		}
		if err := s.Set(ctx, key, []byte("v"), ttl); err != nil {
			t.Fatalf("Set(ttl=%v) 失败: %v", ttl, err)
		}
	}
	time.Sleep(30 * time.Millisecond)
	for _, k := range []string{"k", "k-neg"} {
		if _, ok, _ := s.Get(ctx, k); !ok {
			t.Fatalf("%s：ttl<=0 不该过期", k)
		}
	}
}

func testDeletePrefix(t *testing.T, s cache.Store) {
	t.Helper()
	ctx := context.Background()

	keys := []string{"a:1", "a:2", "a:b:3", "b:1"}
	for _, k := range keys {
		if err := s.Set(ctx, k, []byte("v"), time.Minute); err != nil {
			t.Fatalf("Set %s 失败: %v", k, err)
		}
	}
	if err := s.DeletePrefix(ctx, "a:"); err != nil {
		t.Fatalf("DeletePrefix 失败: %v", err)
	}
	for _, k := range []string{"a:1", "a:2", "a:b:3"} {
		if _, ok, _ := s.Get(ctx, k); ok {
			t.Errorf("%s 应被前缀删除", k)
		}
	}
	if _, ok, _ := s.Get(ctx, "b:1"); !ok {
		t.Error("b:1 不该被 a: 前缀波及")
	}
}

func testValueIsCopy(t *testing.T, s cache.Store) {
	t.Helper()
	ctx := context.Background()

	src := []byte("abc")
	if err := s.Set(ctx, "k", src, time.Minute); err != nil {
		t.Fatalf("Set 失败: %v", err)
	}
	// 写完再改原切片：缓存里那份不该跟着变。
	src[0] = 'z'

	got, _, _ := s.Get(ctx, "k")
	if string(got) != "abc" {
		t.Fatalf("写入方后续修改泄漏进了缓存: %q", got)
	}
	// 读出来再改：也不该影响缓存。
	got[0] = 'y'
	again, _, _ := s.Get(ctx, "k")
	if string(again) != "abc" {
		t.Fatalf("读取方对返回值的修改写回了缓存: %q", again)
	}
}

func testConcurrent(t *testing.T, s cache.Store) {
	t.Helper()
	ctx := context.Background()

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			keys := []string{"p:1", "p:2", "q:3"}
			for j := 0; j < 200; j++ {
				k := keys[j%len(keys)]
				_ = s.Set(ctx, k, []byte("v"), time.Minute)
				_, _, _ = s.Get(ctx, k)
				if j%50 == 0 {
					_ = s.DeletePrefix(ctx, "p:")
				}
				_ = s.Delete(ctx, "p:1")
			}
		}(i)
	}
	wg.Wait()
}

// Open 的推断规则：留空走内存，redis:// 走 Redis，认不出来要报错而不是猜。
func TestOpenInference(t *testing.T) {
	ctx := context.Background()

	for _, c := range []struct {
		name    string
		driver  string
		spec    string
		wantErr string
	}{
		{name: "留空即内存", spec: ""},
		{name: "显式内存", driver: cache.DriverMemory},
		{name: "显式内存且留连接串", driver: cache.DriverMemory, spec: ""},
		{name: "认不出的形态", spec: "127.0.0.1:6379", wantErr: "认不出缓存后端"},
		{name: "后端名非法", driver: "memcached", wantErr: "不支持的缓存后端"},
		{name: "显式 redis 但没连接串", driver: cache.DriverRedis, wantErr: "连接串为空"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, err := cache.Open(ctx, cache.Options{Driver: c.driver, Spec: c.spec})
			if c.wantErr != "" {
				if err == nil {
					t.Fatalf("期望报错 %q，实际拿到 %T", c.wantErr, s)
				}
				if !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("报错里没有 %q: %v", c.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("不该报错: %v", err)
			}
			defer s.Close()
			if _, ok := s.(*cache.Memory); !ok {
				t.Fatalf("期望内存后端，得到 %T", s)
			}
		})
	}
}
