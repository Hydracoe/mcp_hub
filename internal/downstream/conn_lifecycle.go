package downstream

import (
	"context"
	"fmt"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
)

// DefaultMaxInflight 是单个下游允许的并发调用上限。
//
// 存在的理由：一个卡住的下游不该把网关的 goroutine 全部占满。超出上限的调用
// 先排队，再由 ctx 决定是等待还是放弃。
const DefaultMaxInflight = 16

// drainPollInterval 是 Drain 检查在飞调用的间隔。
//
// 用轮询而不是条件变量：Drain 是低频的管理操作（用户点一下停用），每次最多
// 浪费几十个 tick，换来的是不必维护一套唤醒机制——而唤醒机制出错时的症状
// （等不到在飞归零）恰好是最难排查的那种。
const drainPollInterval = 20 * time.Millisecond

// acquire 取出一份可用的 client 引用，并占用一个并发额度。
//
// 未连接时先尝试连接：连接池是懒建的，调用路径不该因为"探活还没跑完"而失败。
// 连接失败即以结构化错误返回，不能让调用方陷入等待。
func (c *Conn) acquire(ctx context.Context) (*mcpclient.Client, error) {
	c.mu.RLock()
	closed := c.closed
	client := c.client
	c.mu.RUnlock()

	if closed {
		return nil, ErrClosed
	}
	// 停用收尾期间不再接新调用，但在飞的那些照常跑完：
	// "拒绝新来的"与"不打断已有的"是同一个决定的两面。
	if c.draining.Load() {
		return nil, &CallError{Kind: KindDraining, Stage: StageCall, Err: ErrDraining}
	}
	if client == nil {
		if _, err := c.Dial(ctx); err != nil {
			return nil, err
		}
		c.mu.RLock()
		client = c.client
		c.mu.RUnlock()
	}
	if client == nil {
		return nil, &CallError{Kind: KindUnknown, Stage: StageConnect, Err: ErrClosed}
	}

	select {
	case c.sem <- struct{}{}:
		return client, nil
	case <-ctx.Done():
		return nil, &CallError{Kind: KindTimeout, Stage: StageCall, Err: ctx.Err()}
	}
}

// release 归还并发额度。
func (c *Conn) release() { <-c.sem }

// Inflight 返回当前正在执行的调用数。
//
// 直接读 sem 的占用数，不另设计数器：多一个计数器就多一处可能与它不同步的
// 状态，而在飞数算错会直接导致停用等错时机。
func (c *Conn) Inflight() int { return len(c.sem) }

// beginCall 为一次调用准备可取消的上下文，并登记到在飞集合。
//
// 登记是为了让 Close 能撤销它。把连接从池里摘掉并不等于调用会停下来——已经
// 发出的 HTTP 请求有它自己的生命周期。没有这一步，"等到上限就强关"只是一句
// 空话：连接关了，调用还挂着，界面上的"停用中"永远不会结束。
func (c *Conn) beginCall(ctx context.Context) (context.Context, func()) {
	callCtx, cancel := context.WithCancel(ctx)

	c.cancelMu.Lock()
	c.nextCancelID++
	id := c.nextCancelID
	c.inflightCancels[id] = cancel
	c.cancelMu.Unlock()

	return callCtx, func() {
		c.cancelMu.Lock()
		delete(c.inflightCancels, id)
		c.cancelMu.Unlock()
		cancel()
	}
}

// cancelInflight 取消全部在飞调用。只在关闭连接时调用。
func (c *Conn) cancelInflight() {
	c.cancelMu.Lock()
	cancels := make([]context.CancelFunc, 0, len(c.inflightCancels))
	for _, fn := range c.inflightCancels {
		cancels = append(cancels, fn)
	}
	c.inflightCancels = make(map[uint64]context.CancelFunc)
	c.cancelMu.Unlock()

	for _, fn := range cancels {
		fn()
	}
}

// Drain 停止接受新调用，等在飞调用自然结束，然后关闭连接。
//
// 等待期间不打断任何已发出的请求——这是"优雅停用"的全部意义。ctx 决定愿意等
// 多久，到期返回 ctx.Err()，是否强关留给调用方判断：等多久算太久是策略，
// 不该由连接层决定。
func (c *Conn) Drain(ctx context.Context) error {
	c.draining.Store(true)

	ticker := time.NewTicker(drainPollInterval)
	defer ticker.Stop()

	for {
		if c.Inflight() == 0 {
			return c.Close()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// Close 关闭连接并拒绝后续调用。可重复调用。
func (c *Conn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return nil
	}
	c.closed = true

	// 取消在飞调用。正常收尾（Drain 等到归零）时这里没有东西可取消；
	// 只有超时强关才会走到，而那时不取消就等于没关。
	c.cancelInflight()

	// 取消生命周期 ctx 会连带回收 stdio 子进程，即使 client.Close 因故没清干净。
	defer c.lifeCancel()

	if c.client == nil {
		return nil
	}
	err := c.client.Close()
	// client.Close 不回收 Transport 空闲池里的 socket，主动关掉，
	// 避免旧下游的连接挂满 90s 的 IdleConnTimeout。
	if c.rec != nil {
		c.rec.CloseIdleConnections()
	}
	c.client = nil
	c.rec = nil
	if err != nil {
		return fmt.Errorf("关闭下游连接失败: %w", err)
	}
	return nil
}
