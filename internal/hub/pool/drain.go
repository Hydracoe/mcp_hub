package pool

import (
	"context"

	"github.com/hydra/mcp-hub/internal/downstream"
	"go.uber.org/zap"
)

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
func (p *Pool) retireConn(serverID, serverKey string, conn *downstream.Conn) {
	limit := p.drainTimeout()

	// 没有在飞调用时不必登记：收尾会瞬间完成，
	// 让它在管理接口上闪一下"停用中"反而是噪音。
	if inflight := conn.Inflight(); inflight > 0 {
		r := &retirement{
			serverID:  serverID,
			serverKey: serverKey,
			conn:      conn,
			startedAt: p.now(),
			deadline:  p.now().Add(limit),
			done:      make(chan struct{}),
		}
		p.mu.Lock()
		p.retiring[serverID] = r
		p.mu.Unlock()

		p.log.Info("连接进入收尾，等待在飞调用结束",
			zap.String("server", serverKey), zap.Int("inflight", inflight), zap.Duration("limit", limit))
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), limit)
		defer cancel()

		err := conn.Drain(ctx)

		p.mu.Lock()
		r := p.retiring[serverID]
		delete(p.retiring, serverID)
		p.mu.Unlock()
		if r != nil {
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
func (p *Pool) Draining() []DrainingInfo {
	p.mu.RLock()
	defer p.mu.RUnlock()

	out := make([]DrainingInfo, 0, len(p.retiring))
	for _, r := range p.retiring {
		out = append(out, DrainingInfo{
			ServerID:  r.serverID,
			ServerKey: r.serverKey,
			Inflight:  r.conn.Inflight(),
			StartedAt: r.startedAt,
			Deadline:  r.deadline,
		})
	}
	return out
}

// DrainingOf 返回某个 server 的收尾状态，无收尾时返回 false。
func (p *Pool) DrainingOf(serverID string) (DrainingInfo, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	r, ok := p.retiring[serverID]
	if !ok {
		return DrainingInfo{}, false
	}
	return DrainingInfo{
		ServerID:  r.serverID,
		ServerKey: r.serverKey,
		Inflight:  r.conn.Inflight(),
		StartedAt: r.startedAt,
		Deadline:  r.deadline,
	}, true
}
