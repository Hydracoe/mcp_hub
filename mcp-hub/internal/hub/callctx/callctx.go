// Package callctx 承载一次工具调用的上下文：谁调用的（接入 key），
// 以及这次调用最终用了哪把下游凭据。两者都走 context 而不是改函数签名，
// 放在独立叶子包，避免鉴权中间件、连接池与聚合器之间循环引用。
package callctx

import (
	"context"
	"sync"
)

// Caller 是一次调用的调用方标识。
// 名字在鉴权时随 id 一起抄下，落日志时不再查 api_keys：归因不随改名而变。
type Caller struct {
	ID   string
	Name string
}

type callerKey struct{}

// WithCaller 标记一次请求的调用方（id + 当时的名字）。id 为空时原样返回，
// 匿名调用不产生额外的 ctx 层级。
func WithCaller(ctx context.Context, id, name string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, callerKey{}, Caller{ID: id, Name: name})
}

// CallerOf 取出调用方，匿名调用返回零值。
func CallerOf(ctx context.Context) Caller {
	if ctx == nil {
		return Caller{}
	}
	c, _ := ctx.Value(callerKey{}).(Caller)
	return c
}

// CallerID 读出调用方 id，匿名调用返回空串。
func CallerID(ctx context.Context) string { return CallerOf(ctx).ID }

// CallerName 读出调用方的名字，匿名调用返回空串。
func CallerName(ctx context.Context) string { return CallerOf(ctx).Name }

// Meta 承载一次调用的运行信息，由连接池写入、聚合器读取。
type Meta struct {
	mu           sync.Mutex
	credentialID string
	attempts     int
}

type metaKey struct{}

// WithMeta 在 ctx 上挂一份调用记录，返回给连接池回填。
func WithMeta(ctx context.Context) (context.Context, *Meta) {
	m := &Meta{}
	return context.WithValue(ctx, metaKey{}, m), m
}

// MetaFrom 取出调用记录，不存在时返回 nil。
func MetaFrom(ctx context.Context) *Meta {
	if ctx == nil {
		return nil
	}
	m, _ := ctx.Value(metaKey{}).(*Meta)
	return m
}

// Record 记下这次调用最终用的凭据与尝试次数。
func (m *Meta) Record(credentialID string, attempt int) {
	m.mu.Lock()
	m.credentialID = credentialID
	m.attempts = attempt
	m.mu.Unlock()
}

// Snapshot 读回凭据与尝试次数。
func (m *Meta) Snapshot() (string, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.credentialID, m.attempts
}
