package pool

import (
	"errors"
	"io"
	"time"

	"github.com/hydra/mcp-hub/internal/downstream"
)

// Failure 返回某个 server 当前的失败状态，无失败时返回零值。
func (p *Pool) Failure(serverID string) (count int, until time.Time, ok bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	f, exists := p.fail[serverID]
	if !exists {
		return 0, time.Time{}, false
	}
	return f.count, f.until, true
}

// recordSuccess 清空失败计数。
func (p *Pool) recordSuccess(serverID string) {
	p.mu.Lock()
	delete(p.fail, serverID)
	p.mu.Unlock()
}

// recordFailure 累加失败并推进退避窗口。
// timeout 不立即进入退避：单个慢工具会把整条连接误判为不可用，
// 而慢不等于坏；它只累加计数，留给后续失败决策。
func (p *Pool) recordFailure(serverID string, kind downstream.ErrorKind, detail string, timeout bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	f := p.fail[serverID]
	if f == nil {
		f = &failure{}
		p.fail[serverID] = f
	}
	f.count++
	f.kind = kind
	f.detail = detail
	if timeout && f.count < 3 {
		f.until = time.Time{}
		return
	}

	wait := backoffInitial << min(f.count-1, 4)
	if wait > backoffMax {
		wait = backoffMax
	}
	f.until = p.now().Add(wait)
}

// isConnLost 判断错误是否意味着底层连接已不可用。
// 只认传输层断裂信号：这些情况下请求多半没有送达，重建连接是安全的。
func isConnLost(err error) bool {
	if errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, downstream.ErrClosed) {
		return true
	}
	// stdio 进程退出也会让连接不可用，分类器已把它归到 EXIT_EARLY。
	return downstream.CallErrorKind(err) == downstream.KindExitEarly
}
