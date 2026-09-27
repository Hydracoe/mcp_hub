package cache

import (
	"context"
	"strings"
	"sync"
	"time"
)

// Memory 是进程内缓存，实现 Store 接口。
//
// 它是桌面端（单进程、零外部依赖）的生产后端，不是测试替身——
// 测试替身在 cachetest，那个实现刻意保持"最小 map"以便一眼看懂；
// 这个实现要把包级契约真正落地，所以多了过期清扫与分片锁。
//
// 与 Redis 的语义差异只有一条，但它很关键：
//
//	**不跨进程共享。** 同一台机器上跑两个 mcp-hub 进程时，各自的
//	内存缓存是两份，任一侧的 Invalidate 都不会影响另一侧。所以
//	部署形态是"多进程 / 多实例"时必须用 Redis。这条约束由配置层
//	提示（见 config 的缓存后端说明），不是运行时能兜住的。
//
// 其余语义与 Redis 对齐：
//   - ttl > 0 过期；ttl <= 0 不过期；
//   - DeletePrefix 删掉所有以该前缀开头的 key；
//   - 所有方法并发安全。
type Memory struct {
	shards [memShards]*memShard

	// stopGC 结束后台清扫协程。
	stopGC    chan struct{}
	closeOnce sync.Once
}

const memShards = 16

// 后台清扫间隔：过期条目在 Get 时也会被顺手删掉，这个协程只是
// 让"写了没人读"的 key 不至于永久占着内存。
const memGCInterval = 5 * time.Minute

type memItem struct {
	value   []byte
	expires time.Time // 零值表示不过期
}

func (i memItem) expired(now time.Time) bool {
	return !i.expires.IsZero() && now.After(i.expires)
}

type memShard struct {
	mu    sync.Mutex
	items map[string]memItem
}

// MemoryOptions 是构造参数。
type MemoryOptions struct {
	// GCInterval 是后台清扫间隔，<= 0 用默认值。
	// 测试里传一个很小的值就能验证清扫路径。
	GCInterval time.Duration
}

// NewMemory 创建一个进程内缓存并启动后台清扫。
// 用完要 Close，否则清扫协程会一直活着。
func NewMemory(opts MemoryOptions) *Memory {
	m := &Memory{stopGC: make(chan struct{})}
	for i := range m.shards {
		m.shards[i] = &memShard{items: make(map[string]memItem)}
	}

	interval := opts.GCInterval
	if interval <= 0 {
		interval = memGCInterval
	}
	go m.sweepLoop(interval)
	return m
}

// shardOf 按 key 的哈希选分片。用 FNV 而不是 crypto 哈希：
// 这里只需要"分布均匀"，不需要抗碰撞。
func (m *Memory) shardOf(key string) *memShard {
	var h uint32 = 2166136261
	for i := 0; i < len(key); i++ {
		h ^= uint32(key[i])
		h *= 16777619
	}
	return m.shards[h%memShards]
}

func (m *Memory) Get(_ context.Context, key string) ([]byte, bool, error) {
	s := m.shardOf(key)
	s.mu.Lock()
	defer s.mu.Unlock()

	it, ok := s.items[key]
	if !ok {
		return nil, false, nil
	}
	if it.expired(time.Now()) {
		delete(s.items, key)
		return nil, false, nil
	}
	// 复制一份出去：调用方拿到的是自己的副本，之后对它的修改
	// 不会绕过接口改到缓存里（Redis 天然就是副本语义，这里要对齐）。
	return append([]byte(nil), it.value...), true, nil
}

func (m *Memory) Set(_ context.Context, key string, value []byte, ttl time.Duration) error {
	var exp time.Time
	if ttl > 0 {
		exp = time.Now().Add(ttl)
	}
	s := m.shardOf(key)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items[key] = memItem{value: append([]byte(nil), value...), expires: exp}
	return nil
}

func (m *Memory) Delete(_ context.Context, keys ...string) error {
	for _, k := range keys {
		s := m.shardOf(k)
		s.mu.Lock()
		delete(s.items, k)
		s.mu.Unlock()
	}
	return nil
}

func (m *Memory) DeletePrefix(_ context.Context, prefix string) error {
	// 前缀可以横跨任意分片，所以每个分片都要扫一遍。
	// 单进程内的 key 数量在千级，全扫是微秒级。
	for _, s := range m.shards {
		s.mu.Lock()
		for k := range s.items {
			if strings.HasPrefix(k, prefix) {
				delete(s.items, k)
			}
		}
		s.mu.Unlock()
	}
	return nil
}

// Close 停止后台清扫。可重复调用。
func (m *Memory) Close() error {
	m.closeOnce.Do(func() { close(m.stopGC) })
	return nil
}

func (m *Memory) sweepLoop(interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-m.stopGC:
			return
		case <-t.C:
			m.sweep()
		}
	}
}

func (m *Memory) sweep() {
	now := time.Now()
	for _, s := range m.shards {
		s.mu.Lock()
		for k, it := range s.items {
			if it.expired(now) {
				delete(s.items, k)
			}
		}
		s.mu.Unlock()
	}
}

// Len 返回当前条目数（含未过期与已过期未清理的）。
// 只给测试与诊断用——正常路径不该依赖缓存里"有多少条"。
func (m *Memory) Len() int {
	n := 0
	for _, s := range m.shards {
		s.mu.Lock()
		n += len(s.items)
		s.mu.Unlock()
	}
	return n
}
