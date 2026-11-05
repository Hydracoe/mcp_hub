// Package cachetest 提供测试用的内存 cache.Store 实现。
// 最小 map 实现，让不连 Redis 的单测也能覆盖 Typed、失效路径等纯逻辑。
package cachetest

import (
	"context"
	"strings"
	"sync"
	"time"
)

type item struct {
	value   []byte
	expires time.Time
}

// Store 是测试用的进程内 cache.Store。
type Store struct {
	mu    sync.Mutex
	items map[string]item
}

// New 创建一个空的测试缓存。
func New() *Store { return &Store{items: make(map[string]item)} }

func (s *Store) Get(_ context.Context, key string) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	it, ok := s.items[key]
	if !ok {
		return nil, false, nil
	}
	if !it.expires.IsZero() && time.Now().After(it.expires) {
		delete(s.items, key)
		return nil, false, nil
	}
	return append([]byte(nil), it.value...), true, nil
}

func (s *Store) Set(_ context.Context, key string, value []byte, ttl time.Duration) error {
	var exp time.Time
	if ttl > 0 {
		exp = time.Now().Add(ttl)
	}
	s.mu.Lock()
	s.items[key] = item{value: append([]byte(nil), value...), expires: exp}
	s.mu.Unlock()
	return nil
}

func (s *Store) Delete(_ context.Context, keys ...string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range keys {
		delete(s.items, k)
	}
	return nil
}

func (s *Store) DeletePrefix(_ context.Context, prefix string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k := range s.items {
		if strings.HasPrefix(k, prefix) {
			delete(s.items, k)
		}
	}
	return nil
}

func (s *Store) Close() error { return nil }
