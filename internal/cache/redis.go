package cache

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// Redis 是分布式实现，生产形态。
//
// 语义严格按包级注释里的契约落地：
//   - ttl > 0 交给 Redis 的 EXPIRE；ttl <= 0 不设过期。
//   - DeletePrefix 用 SCAN MATCH + 批量 UNLINK，不用 KEYS：
//     KEYS 是全表阻塞操作，key 一多就会卡住整个实例。
//   - 多进程可以同时读写同一份 key；DeletePrefix 之后，另一个进程
//     可能立刻写回旧值。调用方不能依赖跨进程强一致。
//
// 所有 key 都带一层全局前缀（默认 mcp-hub:）。与别的应用共享一个
// 实例时，前缀把命名空间隔开，DeletePrefix 也只会清掉自己的前缀。
type Redis struct {
	client    *redis.Client
	keyPrefix string

	closeOnce sync.Once
	closeErr  error
}

// RedisOptions 是打开 Redis 缓存的参数。
type RedisOptions struct {
	// URL 是 redis:// 或 rediss:// 连接串，例如：
	//
	//	redis://127.0.0.1:6379/0
	//	redis://:password@10.0.0.5:6379/2
	//	rediss://host:6380/0  （TLS）
	URL string
	// KeyPrefix 是全局 key 前缀，留空用 mcp-hub:。
	KeyPrefix string
	// PoolSize 是连接池大小，留空用 16，与 PG 连接池一致。
	PoolSize int
}

// NewRedis 打开 Redis 缓存并用一次 Ping 验证连通性。
//
// Ping 失败就返回错误而不是带病启动：缓存连不上不该靠静默降级
// 消化，那只会把"Redis 配错了"变成"缓存好像没生效"。
func NewRedis(ctx context.Context, opts RedisOptions) (*Redis, error) {
	if opts.URL == "" {
		return nil, errors.New("Redis 缓存需要 redis:// 连接串")
	}
	opt, err := redis.ParseURL(opts.URL)
	if err != nil {
		return nil, fmt.Errorf("解析 Redis 连接串失败: %w", err)
	}
	if opts.PoolSize > 0 {
		opt.PoolSize = opts.PoolSize
	} else {
		opt.PoolSize = 16
	}
	if opt.DialTimeout == 0 {
		opt.DialTimeout = 5 * time.Second
	}
	// 调用方传入的 ctx 超时优先于命令级超时。
	opt.ContextTimeoutEnabled = true

	client := redis.NewClient(opt)

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := client.Ping(pingCtx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("连接 Redis 失败: %w", err)
	}

	prefix := opts.KeyPrefix
	if prefix == "" {
		prefix = "mcp-hub:"
	}
	return &Redis{client: client, keyPrefix: prefix}, nil
}

// Get 实现 Store。未命中返回 false，不返回错误。
func (r *Redis) Get(ctx context.Context, key string) ([]byte, bool, error) {
	val, err := r.client.Get(ctx, r.key(key)).Bytes()
	switch {
	case errors.Is(err, redis.Nil):
		return nil, false, nil
	case err != nil:
		return nil, false, fmt.Errorf("读取缓存键失败: %w", err)
	}
	return val, true, nil
}

// Set 实现 Store。
func (r *Redis) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	if ttl < 0 {
		ttl = 0
	}
	if err := r.client.Set(ctx, r.key(key), value, ttl).Err(); err != nil {
		return fmt.Errorf("写入缓存键失败: %w", err)
	}
	return nil
}

// Delete 实现 Store。删不存在的 key 不算错误。
func (r *Redis) Delete(ctx context.Context, keys ...string) error {
	if len(keys) == 0 {
		return nil
	}
	full := make([]string, 0, len(keys))
	for _, k := range keys {
		full = append(full, r.key(k))
	}
	if err := r.client.Del(ctx, full...).Err(); err != nil {
		return fmt.Errorf("删除缓存键失败: %w", err)
	}
	return nil
}

// DeletePrefix 实现 Store。
//
// SCAN 按游标增量取，key 再多也不会阻塞 Redis；UNLINK 在 Redis
// 内部异步释放内存，对大 value 比 DEL 更轻。
func (r *Redis) DeletePrefix(ctx context.Context, prefix string) error {
	pattern := r.keyPrefix + prefix + "*"

	var cursor uint64
	for {
		keys, next, err := r.client.Scan(ctx, cursor, pattern, 512).Result()
		if err != nil {
			return fmt.Errorf("扫描缓存键失败: %w", err)
		}
		if len(keys) > 0 {
			if err := r.client.Unlink(ctx, keys...).Err(); err != nil {
				return fmt.Errorf("删除缓存键失败: %w", err)
			}
		}
		cursor = next
		if cursor == 0 {
			return nil
		}
	}
}

// Close 释放连接池。可重复调用：只有第一次真正关闭，
// 之后返回第一次的结果，不报"client is closed"。
func (r *Redis) Close() error {
	r.closeOnce.Do(func() { r.closeErr = r.client.Close() })
	return r.closeErr
}

func (r *Redis) key(k string) string { return r.keyPrefix + k }
