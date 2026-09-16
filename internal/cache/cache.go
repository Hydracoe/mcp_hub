// Package cache 是缓存抽象层。
//
// 生产的唯一实现是 Redis（redis.go）；测试用的进程内假实现见 cachetest。
// 接口刻意停在"字节 + TTL + 前缀删除"这一层，不暴露任何单一
// 后端的假设，换实现不改调用方。
//
// 接 Redis 时需要满足的语义（写在这里是为了换实现时不用回头翻代码）：
//   - Set 的 ttl > 0 必须真的过期；ttl <= 0 表示不设过期。
//   - DeletePrefix 要删掉所有以该前缀开头的 key。Redis 侧用
//     SCAN MATCH <prefix>* + 分批 DEL，别用 KEYS。
//   - 所有方法并发安全，且允许多进程同时读写同一份 key——
//     也就是说 DeletePrefix 之后，另一个进程可能立刻写回旧值。
//     调用方不能把"删了就一定读到新值"当成跨进程的保证。
package cache

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"sync"
	"time"
)

// Store 是缓存后端。实现必须并发安全。
type Store interface {
	// Get 返回 key 的值。第二个返回值为 false 表示未命中或已过期。
	Get(ctx context.Context, key string) ([]byte, bool, error)

	// Set 写入一个值。ttl <= 0 表示不过期。
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error

	// Delete 删除若干 key。删不存在的 key 不算错误。
	Delete(ctx context.Context, keys ...string) error

	// DeletePrefix 删除所有以 prefix 开头的 key。
	DeletePrefix(ctx context.Context, prefix string) error

	// Close 释放后端资源。
	Close() error
}

// Typed 给一个 Store 加上"key 前缀 + 值类型"两件事。
//
// 前缀不是可选的好习惯：缓存 key 会跨模块重名，而重名导致的
// 类型不匹配（把 server 列表解码成调用统计）会以一种很难回溯的
// 方式失败——解码报错被当成"未命中"，于是缓存看起来只是"总也不命中"。
type Typed[T any] struct {
	store  Store
	prefix string

	// 分片锁：同一 key 的并发回源只放一个进去。
	// 用固定条数的锁而不是 map[key]*Mutex——后者要为无界的 key
	// 集合维护一张同样无界的锁表。
	locks [lockStripes]sync.Mutex
}

const lockStripes = 16

// NewTyped 绑定一个前缀与类型。
func NewTyped[T any](store Store, prefix string) *Typed[T] {
	return &Typed[T]{store: store, prefix: prefix}
}

// Store 返回底层后端。
func (t *Typed[T]) Store() Store { return t.store }

// Get 取一个值，未命中返回 false。
func (t *Typed[T]) Get(ctx context.Context, key string) (T, bool) {
	var zero T
	raw, ok, err := t.store.Get(ctx, t.key(key))
	if err != nil || !ok {
		return zero, false
	}
	var out T
	if err := json.Unmarshal(raw, &out); err != nil {
		// 解不出来就当没有：缓存里的东西永远是可再生的，
		// 为它报错只会把一次缓存损坏放大成一次接口失败。
		return zero, false
	}
	return out, true
}

// Set 写入一个值。
func (t *Typed[T]) Set(ctx context.Context, key string, value T, ttl time.Duration) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("序列化缓存值失败: %w", err)
	}
	return t.store.Set(ctx, t.key(key), raw, ttl)
}

// GetOrLoad 先查缓存，未命中则调用 load 回源并写入。
//
// 同一个 key 上的并发回源会被串行化：否则一个冷 key 在高并发下
// 会让 N 个请求同时打向数据库，缓存反而放大了压力。
func (t *Typed[T]) GetOrLoad(
	ctx context.Context,
	key string,
	ttl time.Duration,
	load func(context.Context) (T, error),
) (T, error) {
	if v, ok := t.Get(ctx, key); ok {
		return v, nil
	}

	unlock := t.lock(key)
	defer unlock()

	// 二次检查：等锁期间别人可能已经回源完成。
	if v, ok := t.Get(ctx, key); ok {
		return v, nil
	}

	v, err := load(ctx)
	if err != nil {
		// 回源失败不写缓存。把一次瞬时故障固化成一个 TTL 那么长的
		// 持续故障，是缓存最常见的自我伤害方式。
		return v, err
	}
	// 写失败只是让下次继续回源，不该把已经拿到的数据丢掉。
	_ = t.Set(ctx, key, v, ttl)
	return v, nil
}

// Invalidate 删掉指定的 key。
func (t *Typed[T]) Invalidate(ctx context.Context, keys ...string) error {
	if len(keys) == 0 {
		return nil
	}
	full := make([]string, 0, len(keys))
	for _, k := range keys {
		full = append(full, t.key(k))
	}
	return t.store.Delete(ctx, full...)
}

// InvalidateAll 删掉本前缀下的全部 key。
func (t *Typed[T]) InvalidateAll(ctx context.Context) error {
	return t.store.DeletePrefix(ctx, t.prefix+":")
}

func (t *Typed[T]) key(k string) string { return t.prefix + ":" + k }

func (t *Typed[T]) lock(key string) func() {
	h := fnv.New32a()
	_, _ = h.Write([]byte(t.key(key)))
	m := &t.locks[h.Sum32()%lockStripes]
	m.Lock()
	return m.Unlock
}
