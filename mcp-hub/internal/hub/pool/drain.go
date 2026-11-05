package pool

import (
	"context"

	"go.uber.org/zap"
)

// drainConn 是收尾所需的一小部分连接能力。
//
// 抽成接口是为了让收尾流程可测：测试必须能精确控制 Drain 的返回时机，
// 而真实连接的收尾时序取决于网络与在飞调用，测试控制不了。
type drainConn interface {
	// Inflight 返回当前在飞调用数。
	Inflight() int
	// Drain 停止接受新调用，等在飞调用自然结束后关闭连接。
	Drain(ctx context.Context) error
	// Close 强制关闭连接。
	Close() error
}

// Retire 让一条连接优雅退场：先从池中摘除，再等它在飞调用跑完才关。
//
// 摘除是立刻的（从这一刻起新调用不会再走到它上面），等的是已经在跑的
// 那些——它们不该因为运维点了一下开关就被打断，有副作用的工具被拦腰
// 截断可能留下不一致状态。收尾在后台 goroutine 里进行，不阻塞调用方。
func (p *Pool) Retire(serverID string) {
	p.mu.Lock()
	entry := p.conns[serverID]
	delete(p.conns, serverID)
	delete(p.fail, serverID)
	p.mu.Unlock()

	if entry == nil {
		return
	}
	p.retireConn(serverID, entry.spec.Key, entry.conn)
}

// retireConn 接手一条已从池中摘除的连接，后台等它收尾。
//
// 同一个 server 可能同时有多条连接在收尾：重复停用、连接指纹变化后
// 旧连接被淘汰，都是这条路。所以每条收尾记录都有自己的唯一身份，
// 登记与销账一一对应：goroutine 只销自己那一条，绝不碰别人的。
func (p *Pool) retireConn(serverID, serverKey string, conn drainConn) {
	limit := p.drainTimeout()

	// 没有在飞调用时不必登记：收尾会瞬间完成，
	// 让它在管理接口上闪一下"停用中"反而是噪音。
	var r *retirement
	if inflight := conn.Inflight(); inflight > 0 {
		r = &retirement{
			serverID:  serverID,
			serverKey: serverKey,
			conn:      conn,
			startedAt: p.now(),
			deadline:  p.now().Add(limit),
			done:      make(chan struct{}),
		}

		p.mu.Lock()
		p.nextRetireID++
		r.id = p.nextRetireID
		p.retiring[r.id] = r
		p.mu.Unlock()

		p.log.Info("连接进入收尾，等待在飞调用结束",
			zap.String("server", serverKey), zap.Int("inflight", inflight), zap.Duration("limit", limit))
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), limit)
		defer cancel()

		err := conn.Drain(ctx)

		// 只销自己那一条记录。按 serverId 销账会踩到别人：后一次退役
		// 会把前一次的记录删掉并关掉它的完成信号——真正在收尾的那条
		// 永远等不到自己的信号，等它的调用方就永久挂住了。
		//
		// close 放在锁外：关通道可能唤醒等待者，持有池锁时唤醒会让
		// 等待者立刻回头拿池锁，白白拖住整池。
		if r != nil {
			p.mu.Lock()
			delete(p.retiring, r.id)
			p.mu.Unlock()
			close(r.done)
		}

		if err != nil {
			// 等到上限还没收完：调用多半卡在下游了。强关是唯一出路，
			// 但要留下明确痕迹——不然那些调用会以"未知错误"收场，
			// 没人能把它们和这次停用联系起来。
			still := conn.Inflight()
			p.log.Warn("连接收尾超过等待上限，强制关闭",
				zap.String("server", serverKey), zap.Duration("waited", limit), zap.Int("inflight", still),
				zap.String("reason", "等待在飞调用超时，这些调用会被标记为 CANCELED"))
			_ = conn.Close()
			return
		}
		p.log.Info("连接已收尾关闭", zap.String("server", serverKey))
	}()
}

// Draining 返回正在收尾的连接，供管理接口展示停用进度。
//
// 同一个 server 的多条收尾合成一条：管理接口问的是"这个 server 还在
// 停用中吗"，不是"有几条连接在收尾"。
func (p *Pool) Draining() []DrainingInfo {
	p.mu.RLock()
	defer p.mu.RUnlock()

	seen := make(map[string]bool, len(p.retiring))
	out := make([]DrainingInfo, 0, len(p.retiring))
	for _, r := range p.retiring {
		if seen[r.serverID] {
			continue
		}
		seen[r.serverID] = true
		if info, ok := p.drainInfoLocked(r.serverID); ok {
			out = append(out, info)
		}
	}
	return out
}

// DrainingOf 返回某个 server 的收尾状态，无收尾时返回 false。
func (p *Pool) DrainingOf(serverID string) (DrainingInfo, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	return p.drainInfoLocked(serverID)
}

// drainInfoLocked 归并某个 server 的全部收尾记录。
//
// 在飞数相加（每条记录各有各的在飞调用），起始时刻取最早（"停用中"
// 从第一次退役算起），截止时刻取最晚（要等所有连接都收完才算完）。
// 调用方必须已持有池锁。
func (p *Pool) drainInfoLocked(serverID string) (DrainingInfo, bool) {
	var out DrainingInfo
	found := false
	for _, r := range p.retiring {
		if r.serverID != serverID {
			continue
		}
		if !found {
			out = DrainingInfo{
				ServerID:  r.serverID,
				ServerKey: r.serverKey,
				StartedAt: r.startedAt,
				Deadline:  r.deadline,
			}
			found = true
		}
		out.Inflight += r.conn.Inflight()
		if r.startedAt.Before(out.StartedAt) {
			out.StartedAt = r.startedAt
		}
		if r.deadline.After(out.Deadline) {
			out.Deadline = r.deadline
		}
	}
	return out, found
}
