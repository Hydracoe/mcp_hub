package cache

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"
)

// Redis 的语义测试只在能连上真实 Redis 时运行：
// 地址由 MCPHUB_REDIS_URL 指定，缺省 redis://127.0.0.1:6379/15
// （用 15 号库做试验田，避免动到默认库的数据）。
func openTestRedis(t *testing.T) *Redis {
	t.Helper()

	url := os.Getenv("MCPHUB_REDIS_URL")
	if url == "" {
		url = "redis://127.0.0.1:6379/15"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	r, err := NewRedis(ctx, RedisOptions{URL: url})
	if err != nil {
		t.Skipf("Redis 不可用（%s），跳过: %v", url, err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func TestNewRedisRejectsBadSpec(t *testing.T) {
	if _, err := NewRedis(context.Background(), RedisOptions{URL: ""}); err == nil {
		t.Fatal("空连接串应当报错")
	}
	if _, err := NewRedis(context.Background(), RedisOptions{URL: "http://nope"}); err == nil {
		t.Fatal("非 redis 协议的连接串应当报错")
	}
}

func TestRedisSetGetDelete(t *testing.T) {
	r := openTestRedis(t)
	ctx := context.Background()
	prefix := "hub:t1:"
	t.Cleanup(func() { _ = r.DeletePrefix(ctx, prefix) })

	if _, ok, _ := r.Get(ctx, prefix+"nope"); ok {
		t.Fatal("不存在的 key 不该命中")
	}

	if err := r.Set(ctx, prefix+"a", []byte("1"), 0); err != nil {
		t.Fatalf("Set 失败: %v", err)
	}
	if v, ok, _ := r.Get(ctx, prefix+"a"); !ok || string(v) != "1" {
		t.Fatalf("Get = %q, %v", v, ok)
	}

	if err := r.Delete(ctx, prefix+"a"); err != nil {
		t.Fatalf("Delete 失败: %v", err)
	}
	if _, ok, _ := r.Get(ctx, prefix+"a"); ok {
		t.Fatal("删除后仍命中")
	}
}

// TTL 必须真的过期；ttl=0 的 key 必须不过期。
func TestRedisTTL(t *testing.T) {
	r := openTestRedis(t)
	ctx := context.Background()
	prefix := "hub:t2:"
	t.Cleanup(func() { _ = r.DeletePrefix(ctx, prefix) })

	_ = r.Set(ctx, prefix+"ttl", []byte("v"), 100*time.Millisecond)
	_ = r.Set(ctx, prefix+"forever", []byte("v"), 0)

	time.Sleep(150 * time.Millisecond)
	if _, ok, _ := r.Get(ctx, prefix+"ttl"); ok {
		t.Error("过期的 key 仍命中")
	}
	if _, ok, _ := r.Get(ctx, prefix+"forever"); !ok {
		t.Error("ttl=0 的 key 不该过期")
	}
}

func TestRedisDeletePrefix(t *testing.T) {
	r := openTestRedis(t)
	ctx := context.Background()
	prefix := "hub:t3:"
	t.Cleanup(func() { _ = r.DeletePrefix(ctx, prefix) })

	for _, k := range []string{"servers:all", "servers:1", "stats:24h"} {
		_ = r.Set(ctx, prefix+k, []byte("v"), 0)
	}

	if err := r.DeletePrefix(ctx, prefix+"servers:"); err != nil {
		t.Fatalf("DeletePrefix 失败: %v", err)
	}
	for _, k := range []string{"servers:all", "servers:1"} {
		if _, ok, _ := r.Get(ctx, prefix+k); ok {
			t.Errorf("%s 应被前缀删除", k)
		}
	}
	if _, ok, _ := r.Get(ctx, prefix+"stats:24h"); !ok {
		t.Error("前缀之外的 key 不该被删")
	}
}

// 前缀必须按整段匹配：hub:serversX 不能被 hub:servers: 捎带删掉。
func TestRedisDeletePrefixIsNotStringPrefix(t *testing.T) {
	r := openTestRedis(t)
	ctx := context.Background()
	prefix := "hub:t4:"
	t.Cleanup(func() { _ = r.DeletePrefix(ctx, prefix) })

	_ = r.Set(ctx, prefix+"servers:all", []byte("v"), 0)
	_ = r.Set(ctx, prefix+"serversX", []byte("v"), 0)

	if err := r.DeletePrefix(ctx, prefix+"servers:"); err != nil {
		t.Fatalf("DeletePrefix 失败: %v", err)
	}
	if _, ok, _ := r.Get(ctx, prefix+"serversX"); !ok {
		t.Error("不该把 hub:serversX 一起删掉")
	}
}

// 大批量 key 下 DeletePrefix 也要清干净：
// SCAN 是游标增量，这个用例验证游标一直转到空为止。
func TestRedisDeletePrefixManyKeys(t *testing.T) {
	r := openTestRedis(t)
	ctx := context.Background()
	prefix := "hub:t5:"
	t.Cleanup(func() { _ = r.DeletePrefix(ctx, prefix) })

	const n = 1000
	pipe := r.client.Pipeline()
	for i := 0; i < n; i++ {
		pipe.Set(ctx, prefix+strconv.Itoa(i), []byte("v"), 0)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		t.Fatalf("批量写入失败: %v", err)
	}

	if err := r.DeletePrefix(ctx, prefix); err != nil {
		t.Fatalf("DeletePrefix 失败: %v", err)
	}

	var left int64
	var cursor uint64
	for {
		keys, next, err := r.client.Scan(ctx, cursor, r.keyPrefix+prefix+"*", 512).Result()
		if err != nil {
			t.Fatalf("扫描残留 key 失败: %v", err)
		}
		left += int64(len(keys))
		cursor = next
		if cursor == 0 {
			break
		}
	}
	if left != 0 {
		t.Errorf("仍残留 %d 个 key", left)
	}
}

func TestRedisCloseIsIdempotent(t *testing.T) {
	r := openTestRedis(t)
	if err := r.Close(); err != nil {
		t.Fatalf("Close 失败: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("二次 Close 失败: %v", err)
	}
}
