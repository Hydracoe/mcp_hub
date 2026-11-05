package hub

import (
	"context"
	"math/rand"
	"time"

	"github.com/hydra/mcp-hub/internal/downstream"
	"github.com/hydra/mcp-hub/internal/hub/pool"
	"go.uber.org/zap"
)

// 弃用密钥自动探活救回（O1）的参数。
const (
	// reviveInterval 是自动探活的周期。低于它没有意义：配额类恢复以
	// 月/日计，限流类解除也是分钟级；探得太勤只是白打下游。
	reviveInterval = 15 * time.Minute

	// reviveProbeTimeout 是单把弃用钥探活的超时。探活是只读的
	// tools/list，正常几百毫秒；给足余量但不让一把慢下游拖住整轮。
	reviveProbeTimeout = 30 * time.Second
)

// startKeyReviver 启动弃用密钥的自动探活循环（O1）。
// goroutine 计入 closeWG（构造处已统一 Add）。
func (s *Service) startKeyReviver() {
	// 错峰启动：随机延迟后进入固定周期，避免进程重启或未来多实例
	// 在同一时刻集中探测下游。
	delay := time.Duration(rand.Int63n(int64(reviveInterval)))
	go s.keyReviver(delay)
}

// keyReviver 是自动探活的主循环。
func (s *Service) keyReviver(initialDelay time.Duration) {
	defer s.closeWG.Done()
	timer := time.NewTimer(initialDelay)
	defer timer.Stop()
	for {
		select {
		case <-s.closeCh:
			return
		case <-timer.C:
			s.reviveDisabledKeys()
			timer.Reset(reviveInterval)
		}
	}
}

// reviveDisabledKeys 对"可自动恢复"的弃用密钥做一轮只读探活（O1）。
//
// 只覆盖两类：QUOTA_EXHAUSTED（用户充值后自然恢复，语义最安全）与
// RATE_LIMITED（R16 顶格禁用，账号级限流可能随时间解除）。401/403 类
// 不试——凭据值本身错了，探活救不回来，救回判断必须留给人。
// 探活是只读的（tools/list）：不消耗调用配额、不改工具缓存、探活失败
// 不动任何状态。任何一路出错都只是"这轮没救成"，等下一轮。
func (s *Service) reviveDisabledKeys() {
	ctx, cancel := context.WithTimeout(context.Background(), reviveInterval)
	defer cancel()

	for serverID, list := range s.pool.KeyStatuses() {
		for _, st := range list {
			if st.State != downstream.KeyDisabled {
				continue
			}
			switch st.DisabledKind {
			case downstream.VerdictQuotaExhausted, downstream.VerdictRateLimited:
			default:
				continue
			}
			// 一轮探测内部随时响应停机，不让 Close 干等整轮跑完。
			select {
			case <-s.closeCh:
				return
			default:
			}
			s.tryReviveKey(ctx, serverID, st)
		}
	}
}

// tryReviveKey 尝试救回一把弃用密钥：探活通过则自动启用并发恢复事件。
func (s *Service) tryReviveKey(ctx context.Context, serverID string, st downstream.KeyStatus) {
	cred, err := s.credentialOf(ctx, serverID, st.ID)
	if err != nil || !cred.Enabled || cred.State.KeyState() != downstream.KeyDisabled {
		// 已删除 / 人工停用（Enabled=false 表示人说了算，自动救回不得越权）/
		// 池内快照滞后（库里其实已经活了）：都不归自动救回管。
		return
	}
	sv, err := s.store.Servers.Get(ctx, serverID)
	if err != nil {
		return
	}

	probeCtx, cancel := context.WithTimeout(ctx, reviveProbeTimeout)
	defer cancel()
	probeSpec := sv.Spec
	probeSpec.Headers = cred.Headers
	res := downstream.NewProber(probeSpec).Probe(probeCtx)

	if !res.OK {
		s.log.Info("弃用密钥探活未通过，继续等待下一轮",
			zap.String("server", sv.Spec.Key),
			zap.String("credential", cred.LabelOrFallback()),
			zap.String("disabledKind", string(st.DisabledKind)),
			zap.String("kind", string(res.Kind)),
			zap.String("detail", res.Detail))
		return
	}

	// 探活通过：自动救回。SetCredentialEnabled 会复位持久状态、清冷却并
	// 重灌密钥池。连续限流计数一并清零——救回的前提就是探活已通过，
	// 带着顶格的旧计数回池，下一次 429 会立刻把它再禁掉，形成抖动。
	if err := s.SetCredentialEnabled(ctx, serverID, st.ID, true); err != nil {
		s.log.Error("弃用密钥自动救回失败",
			zap.String("server", sv.Spec.Key),
			zap.String("credential", cred.LabelOrFallback()), zap.Error(err))
		return
	}
	if ring := s.pool.KeyRing(serverID); ring != nil {
		ring.ResetLimitedCount(st.ID)
	}
	s.handleKeyEvent(pool.KeyEvent{
		ServerID:  serverID,
		ServerKey: sv.Spec.Key,
		KeyID:     st.ID,
		KeyLabel:  cred.LabelOrFallback(),
		Verdict:   downstream.VerdictOK,
		Recovered: true,
		Detail:    "探活通过，已自动救回（此前因 " + st.DisabledKind.Human() + " 被禁用）",
		At:        time.Now(),
	})
}
