package pool

import (
	"github.com/hydra/mcp-hub/internal/downstream"
)

// SyncKeyRing 让某个 server 的密钥池与一份最新的持久快照对齐。
//
// 已存在的池子走增量 Sync 并保留冷却与计数——顺手清掉冷却等于开了一个
// "点一下即可绕过限流"的后门。creds 为空表示退回单密钥模式（用
// servers.headers）：密钥池是可选增强，只填一把密钥的用户不该有感知。
func (p *Pool) SyncKeyRing(serverID string, opts downstream.KeyRingOptions, creds []downstream.CredentialInput) {
	p.ringMu.Lock()
	defer p.ringMu.Unlock()

	if len(creds) == 0 {
		delete(p.rings, serverID)
		return
	}
	if existing, ok := p.rings[serverID]; ok {
		existing.Sync(creds)
		return
	}
	p.rings[serverID] = downstream.NewKeyRing(opts, creds)
}

// DropKeyRing 移除某个 server 的密钥池（server 被删除时）。
func (p *Pool) DropKeyRing(serverID string) {
	p.ringMu.Lock()
	delete(p.rings, serverID)
	p.ringMu.Unlock()
}

// KeyRing 返回某个 server 的密钥池，单密钥模式下为 nil。
func (p *Pool) KeyRing(serverID string) *downstream.KeyRing {
	p.ringMu.RLock()
	defer p.ringMu.RUnlock()
	return p.rings[serverID]
}

// KeyRingFor 返回某个 server 的密钥池，附带规格用于展示。
func (p *Pool) KeyRingFor(spec downstream.Spec) *downstream.KeyRing {
	return p.KeyRing(spec.ID)
}

// KeyStatuses 返回全部 server 的密钥运行状态，键为 serverId。
func (p *Pool) KeyStatuses() map[string][]downstream.KeyStatus {
	p.ringMu.RLock()
	defer p.ringMu.RUnlock()

	out := make(map[string][]downstream.KeyStatus, len(p.rings))
	for id, ring := range p.rings {
		out[id] = ring.Status()
	}
	return out
}

// KeyStatusesOf 返回某个 server 的密钥运行状态。
func (p *Pool) KeyStatusesOf(serverID string) []downstream.KeyStatus {
	ring := p.KeyRing(serverID)
	if ring == nil {
		return nil
	}
	return ring.Status()
}

// attemptsFor 计算本次调用的密钥尝试预算，等于可用密钥数。
//
// 只要池里还有可用密钥，密钥级失败（429/401/402/403）就换一把透明重试，
// 调用方无感；池子被彻底打空（全冷却 / 全弃用 / 配额用尽）才返回带
// retryAfter 的 ErrNoUsableKey。可重试的判决都是"立刻被拒"的响应，
// 不等待超时，全试一遍不会拖垮时延。
func (p *Pool) attemptsFor(ring *downstream.KeyRing) int {
	if p.MaxAttempts > 0 {
		return p.MaxAttempts
	}
	n := ring.UsableCount()
	if n < 1 {
		return 1 // 一把都不可用时也要走一次，才能拿到那个带 retryAfter 的错误
	}
	return n
}

// emitKeyEvent 把密钥状态变化交给上层。
func (p *Pool) emitKeyEvent(spec downstream.Spec, lease *downstream.KeyLease, out downstream.KeyOutcome) {
	if p.OnKeyEvent == nil || lease == nil {
		return
	}
	// 只上报真正发生变化的：每次成功都回调会把日志刷满，
	// 而"成功"本来就由调用日志记录。
	if !out.Disabled && out.Cooling <= 0 && !out.CollectiveProtection && !out.Recovered {
		return
	}
	p.OnKeyEvent(KeyEvent{
		ServerID:             spec.ID,
		ServerKey:            spec.Key,
		KeyID:                lease.ID(),
		KeyLabel:             out.KeyLabel,
		Verdict:              out.Verdict,
		State:                out.State,
		Cooling:              out.Cooling,
		CoolingUntil:         out.CoolingUntil,
		Disabled:             out.Disabled,
		Recovered:            out.Recovered,
		CollectiveProtection: out.CollectiveProtection,
		Detail:               out.Detail,
		At:                   p.now(),
	})
}
