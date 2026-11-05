package pool

import (
	"context"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// --- 测试用的假连接 ---

// fakeConn 是可控的假连接：收尾时序由测试说了算，不靠 sleep 抢时间。
//
// 真实的连接什么时候收完取决于网络与在飞调用，测试控制不了；把它换成
// 假的，"什么时候收尾"就变成测试的一行代码。
type fakeConn struct {
	inflight atomic.Int64

	started chan struct{} // Drain 进入时关闭
	release chan struct{} // 测试放行后 Drain 才返回
	drained chan struct{} // Drain 返回时关闭

	startOnce   sync.Once
	drainOnce   sync.Once
	releaseOnce sync.Once
	closed      atomic.Bool
}

func newFakeConn(inflight int) *fakeConn {
	c := &fakeConn{
		started: make(chan struct{}),
		release: make(chan struct{}),
		drained: make(chan struct{}),
	}
	c.inflight.Store(int64(inflight))
	return c
}

// Drain 等测试放行才返回；放行前它一直占着连接。
func (c *fakeConn) Drain(ctx context.Context) error {
	c.startOnce.Do(func() { close(c.started) })
	defer c.drainOnce.Do(func() { close(c.drained) })
	select {
	case <-c.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *fakeConn) Inflight() int { return int(c.inflight.Load()) }

func (c *fakeConn) Close() error {
	c.closed.Store(true)
	return nil
}

// allow 放行收尾。
func (c *fakeConn) allow() {
	c.releaseOnce.Do(func() { close(c.release) })
}

// --- 测试用的日志栅栏 ---

// logBarrier 在有日志写出时发信号。
//
// 它是测试里的栅栏：某条连接打出"连接已收尾关闭"时，说明它的 goroutine
// 已经走完收尾的销账（那句日志在销账之后）。栅栏之后对池状态的断言才是
// 确定的，不必靠 sleep 猜对方走到哪一步了。
type logBarrier struct {
	zapcore.Core
	ch chan zapcore.Entry
}

func (b *logBarrier) With(fields []zapcore.Field) zapcore.Core {
	return &logBarrier{Core: b.Core.With(fields), ch: b.ch}
}

// Check 必须把自己塞回 CheckedEntry：内建 core 的 Check 会把它自己填进去，
// 那样 Write 就绕过这里，栅栏信号也就发不出来。
func (b *logBarrier) Check(e zapcore.Entry, ce *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if b.Enabled(e.Level) {
		return ce.AddCore(e, b)
	}
	return ce
}

func (b *logBarrier) Write(e zapcore.Entry, fields []zapcore.Field) error {
	select {
	case b.ch <- e:
	default:
	}
	return b.Core.Write(e, fields)
}

// newBarrierLogger 造一个会把日志转发到 channel 的 logger。
func newBarrierLogger() (*zap.Logger, chan zapcore.Entry) {
	ch := make(chan zapcore.Entry, 32)
	core := &logBarrier{
		Core: zapcore.NewCore(
			zapcore.NewJSONEncoder(zapcore.EncoderConfig{MessageKey: "msg"}),
			zapcore.AddSync(io.Discard),
			zapcore.DebugLevel,
		),
		ch: ch,
	}
	return zap.New(core), ch
}

// waitLog 等某条日志出现（期间的其它日志跳过）。
func waitLog(t *testing.T, ch chan zapcore.Entry, msg string) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case e := <-ch:
			if e.Message == msg {
				return
			}
		case <-deadline:
			t.Fatalf("等待日志 %q 超时", msg)
		}
	}
}

// --- 测试辅助 ---

// retiringRecords 取当前登记的收尾记录。
func retiringRecords(p *Pool) []*retirement {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]*retirement, 0, len(p.retiring))
	for _, r := range p.retiring {
		out = append(out, r)
	}
	return out
}

// waitDone 等一条收尾记录的完成信号。
func waitDone(t *testing.T, ch chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("等待收尾完成信号超时：这条收尾的信号永远不会被唤醒")
	}
}

// --- 回归测试 ---

// 同一条 server 的第二次退役（没有在飞调用）不能销掉第一次的账：
// 第一次的完成信号必须等它自己收完才关。
//
// 没有在飞调用的那次退役不登记记录，它的 goroutine 曾经会按 serverId
// 把第一条记录删掉并关掉它的信号——真正的收尾随后读到空，永远不会再关，
// 等信号的调用方就永久挂住了。
func TestSecondRetirementDoesNotClearOtherRecord(t *testing.T) {
	logger, logs := newBarrierLogger()
	p := NewPool(logger)

	conn1 := newFakeConn(1) // 有在飞调用：会登记
	conn2 := newFakeConn(0) // 没有在飞调用：不登记

	p.retireConn("srv-1", "key-1", conn1)
	p.retireConn("srv-1", "key-1", conn2)

	records := retiringRecords(p)
	if len(records) != 1 {
		t.Fatalf("登记了 %d 条收尾记录, want 1", len(records))
	}
	r1 := records[0]

	<-conn1.started
	<-conn2.started

	// 第二条立刻收完，然后卡一道栅栏：它已经走完销账。
	conn2.allow()
	waitLog(t, logs, "连接已收尾关闭")

	// 第一条还在收尾：账还在，信号也不能被提前关掉。
	select {
	case <-r1.done:
		t.Fatal("第一条收尾还没完成，完成信号却被第二次退役提前关了")
	default:
	}
	if got := len(retiringRecords(p)); got != 1 {
		t.Errorf("收尾记录数 = %d, want 1（第二次退役不该动第一次的账）", got)
	}
	info, ok := p.DrainingOf("srv-1")
	if !ok {
		t.Fatal("第一条还在收尾，DrainingOf 却说没有收尾")
	}
	if info.Inflight != 1 {
		t.Errorf("在飞调用数 = %d, want 1", info.Inflight)
	}

	// 第一条收完，信号才关，状态才清。
	conn1.allow()
	waitDone(t, r1.done)
	if _, ok := p.DrainingOf("srv-1"); ok {
		t.Error("收尾已经结束，DrainingOf 却还在报告")
	}
}

// 两次都是"有在飞调用"的退役：两条记录各自销账，谁也不能动对方的。
//
// 曾经第二条直接顶掉第一条：先收尾的那个会替后一个关掉信号（报"已完成"
// 的其实是还没收完的那条），后收尾的那个读到空，两条信号都等不到。
func TestParallelRetirementsAreAccountedSeparately(t *testing.T) {
	logger, _ := newBarrierLogger()
	p := NewPool(logger)

	conn1 := newFakeConn(1)
	conn2 := newFakeConn(2)

	p.retireConn("srv-2", "key-2", conn1)
	p.retireConn("srv-2", "key-2", conn2)

	records := retiringRecords(p)
	if len(records) != 2 {
		t.Fatalf("登记了 %d 条收尾记录, want 2（同 server 的两次退役不该互相覆盖）", len(records))
	}
	if records[0].id == records[1].id {
		t.Fatal("两条记录用了同一个身份，销账会互相踩")
	}
	if records[0].done == records[1].done {
		t.Fatal("两条记录共用一个完成信号，后关的那个会 panic")
	}

	conn1.allow()
	conn2.allow()

	// 两条信号都要被唤醒，且各自只关一次（多关会 panic）。
	waitDone(t, records[0].done)
	waitDone(t, records[1].done)

	if got := len(retiringRecords(p)); got != 0 {
		t.Errorf("收尾记录数 = %d, want 0", got)
	}
}

// 停用中的 server 仍要在管理接口上看得见：Draining 与 DrainingOf 都要
// 报出正在收尾的连接，收完后又都要干净。
func TestDrainingReportsOngoingRetirement(t *testing.T) {
	logger, _ := newBarrierLogger()
	p := NewPool(logger)

	conn := newFakeConn(3)
	p.retireConn("srv-3", "key-3", conn)

	records := retiringRecords(p)
	if len(records) != 1 {
		t.Fatalf("登记了 %d 条收尾记录, want 1", len(records))
	}

	all := p.Draining()
	if len(all) != 1 {
		t.Fatalf("Draining 返回 %d 条, want 1", len(all))
	}
	if all[0].ServerID != "srv-3" || all[0].ServerKey != "key-3" {
		t.Errorf("Draining 报的是 %+v, want srv-3/key-3", all[0])
	}
	if all[0].Inflight != 3 {
		t.Errorf("在飞调用数 = %d, want 3", all[0].Inflight)
	}

	info, ok := p.DrainingOf("srv-3")
	if !ok {
		t.Fatal("DrainingOf 查不到 srv-3")
	}
	if info.Inflight != 3 {
		t.Errorf("DrainingOf 的在飞调用数 = %d, want 3", info.Inflight)
	}
	if _, ok := p.DrainingOf("没这个 server"); ok {
		t.Error("不存在的 server 不该有收尾状态")
	}

	conn.allow()
	waitDone(t, records[0].done)

	if got := len(p.Draining()); got != 0 {
		t.Errorf("收尾结束后 Draining 仍有 %d 条, want 0", got)
	}
	if _, ok := p.DrainingOf("srv-3"); ok {
		t.Error("收尾结束后 DrainingOf 还在报告")
	}
}

// 同一个 server 的多条收尾合成一条：在飞数相加，起始时刻取最早。
//
// 管理接口问的是"这个 server 还在停用中吗"，不是"有几条连接在收尾"。
func TestDrainingMergesRecordsOfSameServer(t *testing.T) {
	logger, _ := newBarrierLogger()
	p := NewPool(logger)

	p.retireConn("srv-4", "key-4", newFakeConn(1))
	p.retireConn("srv-4", "key-4", newFakeConn(2))

	all := p.Draining()
	if len(all) != 1 {
		t.Fatalf("Draining 返回 %d 条, want 1（同一 server 合成一条）", len(all))
	}
	if all[0].Inflight != 3 {
		t.Errorf("在飞调用数 = %d, want 3（两条记录相加）", all[0].Inflight)
	}
	info, ok := p.DrainingOf("srv-4")
	if !ok {
		t.Fatal("DrainingOf 查不到 srv-4")
	}
	if info.Inflight != 3 {
		t.Errorf("DrainingOf 的在飞调用数 = %d, want 3", info.Inflight)
	}
}

// 并发退役压力：同一 server 上多条退役同时进行，谁也不能误删别人的账，
// 每条记录的完成信号都要被唤醒。跑在 -race 下必须干净。
func TestConcurrentRetirementsOnSameServer(t *testing.T) {
	logger, _ := newBarrierLogger()
	p := NewPool(logger)

	const n = 32
	conns := make([]*fakeConn, n)
	for i := range conns {
		// 一部分连接没有在飞调用（不登记），一部分有。
		conns[i] = newFakeConn(i % 3)
	}

	var wg sync.WaitGroup
	for i := range conns {
		wg.Add(1)
		go func(c *fakeConn) {
			defer wg.Done()
			p.retireConn("srv-5", "key-5", c)
		}(conns[i])
	}
	wg.Wait()

	records := retiringRecords(p)
	want := 0
	for i := range conns {
		if i%3 != 0 {
			want++
		}
	}
	if len(records) != want {
		t.Fatalf("登记了 %d 条收尾记录, want %d", len(records), want)
	}
	ids := make(map[uint64]bool, len(records))
	for _, r := range records {
		if ids[r.id] {
			t.Fatalf("记录身份 %d 重复了", r.id)
		}
		ids[r.id] = true
	}

	for _, c := range conns {
		c.allow()
	}
	for _, r := range records {
		waitDone(t, r.done)
	}

	if got := len(retiringRecords(p)); got != 0 {
		t.Errorf("收尾记录数 = %d, want 0", got)
	}
}
