package cache

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// Memory 是进程内实现，为换 Redis 之前的过渡形态。
//
// 它有个 Redis 给不了的性质：删了就真的读到新值。接上 Redis 之后
// 这个性质没了（另一个实例可能立刻写回旧值），所以调用方对
// 强一致性的依赖要提前收敛掉——目前的用法都满足：写路径删，
// 读路径允许有几秒的滞后。
type Memory struct {
	mu       sync.RWMutex
	items    map[string]memoryItem
	maxItems int
	now      func() time.Time

	sweepInterval time.Duration
	stop          chan struct{}
	closeOnce     sync.Once
	wg            sync.WaitGroup

	hits      atomic.Int64
	misses    atomic.Int64
	evictions atomic.Int64
}

type memoryItem struct {
	value   []byte
	expires time.Time // 零值表示不过期
}

// Stats 是命中情况，用来回答"缓存到底起没起作用"。
type Stats struct {
	Entries   int   `json:"entries"`
	Bytes     int   `json:"bytes"`
	Hits      int64 `json:"hits"`
	Misses    int64 `json:"misses"`
	Evictions int64 `json:"evictions"`
}

// Option 调整 Memory 的行为。
type Option func(*Memory)

// WithMaxItems 设置条目上限，超出后先清过期项，再淘汰最先到期的。
//
// 默认 1024。刻意有上限：key 里带参数的调用方（比如按 serverId 分键）
// 很容易把 key 空间变成无界的，而进程内缓存泄漏的是内存不是磁盘，
// 出事时没有任何迹象。
func WithMaxItems(n int) Option {
	return func(m *Memory) {
		if n > 0 {
			m.maxItems = n
		}
	}
}

// WithClock 注入时钟。测试用。
func WithClock(now func() time.Time) Option {
	return func(m *Memory) {
		if now != nil {
			m.now = now
		}
	}
}

// WithSweepInterval 设置后台清理间隔，0 表示关掉后台清理
// （过期项改由读写路径惰性清除）。
func WithSweepInterval(d time.Duration) Option {
	return func(m *Memory) {
		m.sweepInterval = d
	}
}

// NewMemory 创建一个进程内缓存。
func NewMemory(opts ...Option) *Memory {
	m := &Memory{
		items:         make(map[string]memoryItem),
		maxItems:      1024,
		now:           time.Now,
		sweepInterval: time.Minute,
		stop:          make(chan struct{}),
	}
	for _, opt := range opts {
		opt(m)
	}
	if m.sweepInterval > 0 {
		m.wg.Add(1)
		go m.sweepLoop(m.sweepInterval)
	}
	return m
}

func (m *Memory) sweepLoop(interval time.Duration) {
	defer m.wg.Done()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-m.stop:
			return
		case <-ticker.C:
			m.purgeExpired()
		}
	}
}

// Get 实现 Store。
func (m *Memory) Get(_ context.Context, key string) ([]byte, bool, error) {
	m.mu.RLock()
	item, ok := m.items[key]
	m.mu.RUnlock()

	if !ok {
		m.misses.Add(1)
		return nil, false, nil
	}
	if item.expired(m.now()) {
		m.deleteIfExpired(key)
		m.misses.Add(1)
		return nil, false, nil
	}

	m.hits.Add(1)
	// 返回副本：缓存里的字节不该被调用方就地改写。
	out := make([]byte, len(item.value))
	copy(out, item.value)
	return out, true, nil
}

func (m *Memory) deleteIfExpired(key string) {
	// 重新取一次再判：读锁与写锁之间别人可能已经换成了新值。
	m.mu.Lock()
	if cur, still := m.items[key]; still && cur.expired(m.now()) {
		delete(m.items, key)
	}
	m.mu.Unlock()
}

// Set 实现 Store。
func (m *Memory) Set(_ context.Context, key string, value []byte, ttl time.Duration) error {
	stored := make([]byte, len(value))
	copy(stored, value)

	item := memoryItem{value: stored}
	if ttl > 0 {
		item.expires = m.now().Add(ttl)
	}

	m.mu.Lock()
	m.items[key] = item
	for len(m.items) > m.maxItems {
		if !m.evictOneLocked() {
			break
		}
	}
	m.mu.Unlock()
	return nil
}

// Delete 实现 Store。删不存在的 key 不算错误。
func (m *Memory) Delete(_ context.Context, keys ...string) error {
	if len(keys) == 0 {
		return nil
	}
	m.mu.Lock()
	for _, k := range keys {
		delete(m.items, k)
	}
	m.mu.Unlock()
	return nil
}

// DeletePrefix 实现 Store。
func (m *Memory) DeletePrefix(_ context.Context, prefix string) error {
	m.mu.Lock()
	for k := range m.items {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			delete(m.items, k)
		}
	}
	m.mu.Unlock()
	return nil
}

// Stats 返回当前命中情况。
func (m *Memory) Stats() Stats {
	m.mu.RLock()
	entries, bytes := len(m.items), 0
	for _, it := range m.items {
		bytes += len(it.value)
	}
	m.mu.RUnlock()

	return Stats{
		Entries:   entries,
		Bytes:     bytes,
		Hits:      m.hits.Load(),
		Misses:    m.misses.Load(),
		Evictions: m.evictions.Load(),
	}
}

// Close 停止后台清理。可重复调用。
func (m *Memory) Close() error {
	m.closeOnce.Do(func() { close(m.stop) })
	m.wg.Wait()
	return nil
}

func (m *Memory) purgeExpired() {
	now := m.now()
	m.mu.Lock()
	for k, it := range m.items {
		if it.expired(now) {
			delete(m.items, k)
		}
	}
	m.mu.Unlock()
}

// evictOneLocked 在持有写锁时丢掉一个条目，返回是否真的丢掉了。
//
// 先扫一遍过期项（那是最该走的），还超就挑最先到期的。
// 永不过期的条目排在最后——它们是"长期有效"，最不该被淘汰。
func (m *Memory) evictOneLocked() bool {
	now := m.now()
	for k, it := range m.items {
		if it.expired(now) {
			delete(m.items, k)
			m.evictions.Add(1)
			return true
		}
	}

	var victim string
	var victimExpiry time.Time
	for k, it := range m.items {
		if victim == "" || before(it.expires, victimExpiry) {
			victim, victimExpiry = k, it.expires
		}
	}
	if victim == "" {
		return false
	}
	delete(m.items, victim)
	m.evictions.Add(1)
	return true
}

func (i memoryItem) expired(now time.Time) bool {
	return !i.expires.IsZero() && !now.Before(i.expires)
}

// before 把零值（永不过期）排在任何具体时刻之后。
func before(a, b time.Time) bool {
	if a.IsZero() {
		return false
	}
	if b.IsZero() {
		return true
	}
	return a.Before(b)
}
