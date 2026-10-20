package hub

import (
	"context"
	"fmt"
	"time"

	"github.com/hydra/mcp-hub/internal/downstream"
	"github.com/hydra/mcp-hub/internal/hub/aggregate"
	"github.com/hydra/mcp-hub/internal/hub/pool"
	"github.com/hydra/mcp-hub/internal/store/credentials"
	"github.com/hydra/mcp-hub/internal/store/servers"
	"go.uber.org/zap"
)

// Reload 从存储重新装载连接配置与聚合快照，不触网。
// 配置变更后调用它即可生效，无需重启进程。
//
// Options.ReloadFn 非空时由它替代本方法：那是测试用的接缝，用来复现
// "写已落库、刷新失败"这条路径（见 reloadAfterCommit）。
func (s *Service) Reload(ctx context.Context) error {
	if s.reloadFn != nil {
		return s.reloadFn(ctx)
	}

	srvs, err := s.store.Servers.List(ctx)
	if err != nil {
		return fmt.Errorf("加载 server 配置失败: %w", err)
	}

	specMap := make(map[string]downstream.Spec, len(srvs))
	specs := make([]downstream.Spec, 0, len(srvs))
	for _, sv := range srvs {
		specMap[sv.Spec.ID] = sv.Spec
		specs = append(specs, sv.Spec)
	}

	s.specs.Lock()
	s.specMap = specMap
	s.specs.Unlock()

	// 连接池与配置对齐：已删除的 server 关闭连接，
	// 剩下的保留——重连代价不该由一次配置变更全体承担。
	s.pool.Sync(specs)

	// 密钥池随配置一起装载，失败不阻断 Reload：没有密钥池时退回单密钥
	// 模式仍然可用，比整个服务起不来要好得多。
	if err := s.syncKeyRings(ctx, srvs); err != nil {
		s.log.Error("装载密钥池失败，这些 server 将退回单密钥模式", zap.Error(err))
	}

	s.refreshAgg()
	return nil
}

// reloadAfterCommit 在存储写入已提交之后刷新运行时配置。
//
// 刷新失败只记日志、不向调用方报错：库是真相，配置已经在里面，
// 下一次 Reload（或重启）会把它装进来；而在这里返回错误会让用户
// 以为没保存成功——配置其实已经写进去了，重试还会撞上唯一约束（409），
// 把一个已经成功的操作显示成失败。
//
// 与 reloadKeyRingAfterCommit 同一个道理：提交之后的刷新属于运行时状态，
// 不是这次写操作本身的结果。
func (s *Service) reloadAfterCommit(ctx context.Context, serverKey, serverID string) {
	if err := s.Reload(ctx); err != nil {
		s.log.Error("运行时配置刷新失败，配置已在库中，下次 Reload 会补装",
			zap.String("server", serverKey),
			zap.String("id", serverID),
			zap.Error(err))
	}
}

// syncKeyRings 把库里的密钥灌进连接池。
// 一次读全部密钥再按 server 分组：逐个查是典型的 N+1，而 Reload 是
// 管理操作的必经路径，不该随 server 数量线性变慢。
func (s *Service) syncKeyRings(ctx context.Context, srvs []servers.Server) error {
	all, err := s.store.Credentials.ListAll(ctx)
	if err != nil {
		return err
	}

	for _, sv := range srvs {
		s.pool.SyncKeyRing(sv.Spec.ID, s.keyRingOptions(sv), toCredentialInputs(all[sv.Spec.ID]))
	}
	return nil
}

// reloadKeyRing 只同步一个 server 的密钥池：密钥增删改是逐 server 的
// 操作，走全量 Reload 会顺带重扫所有 server 与工具缓存。
func (s *Service) reloadKeyRing(ctx context.Context, serverID string) error {
	sv, err := s.store.Servers.Get(ctx, serverID)
	if err != nil {
		return err
	}
	list, err := s.store.Credentials.List(ctx, serverID)
	if err != nil {
		return err
	}
	s.pool.SyncKeyRing(serverID, s.keyRingOptions(sv), toCredentialInputs(list))
	return nil
}

// reloadKeyRingAfterCommit 在存储写入已提交之后同步密钥池。
//
// 同步失败只记日志、不向调用方报错：库是真相，密钥已经在里面，
// 下一次 Reload（或重启）会把它补装进池子；而在这里返回错误会让
// 用户以为没保存成功、重试一遍，重试撞上唯一约束（409），把一个
// 已经成功的操作显示成失败。
func (s *Service) reloadKeyRingAfterCommit(ctx context.Context, serverKey, serverID string) {
	if err := s.reloadKeyRing(ctx, serverID); err != nil {
		s.log.Error("密钥池同步失败，密钥已在库中，下次 Reload 会补装",
			zap.String("server", serverKey), zap.Error(err))
	}
}

// keyRingOptions 汇总密钥池的行为参数。
//
// 只有远程传输能用密钥池：stdio 的凭据在子进程环境变量里，改它等于换进程，
// 做不到按调用轮换。
func (s *Service) keyRingOptions(sv servers.Server) downstream.KeyRingOptions {
	return downstream.KeyRingOptions{
		ServerID:  sv.Spec.ID,
		ServerKey: sv.Spec.Key,
		Now:       time.Now,
	}
}

func toCredentialInputs(list []credentials.Credential) []downstream.CredentialInput {
	if len(list) == 0 {
		return nil
	}
	out := make([]downstream.CredentialInput, 0, len(list))
	for _, c := range list {
		out = append(out, downstream.CredentialInput{
			ID:      c.ID,
			Label:   c.LabelOrFallback(),
			Headers: c.Headers,
			Enabled: c.Enabled,
			State:   c.State.KeyState(),
			Disabled: downstream.DisabledInfo{
				Kind: c.DisabledKind,
				At:   c.DisabledAt,
				Note: c.DisabledNote,
			},
			// O4：把库里的冷却到点时刻带进池子，供新建 entry 时恢复。
			CoolingUntil: c.CoolingUntil,
			RPMLimit:     c.RPMLimit,
		})
	}
	return out
}

// 密钥事件在调用日志里的标识。
// 它们不是"某次调用失败了"，而是"某把密钥的状态变了"。用独立的一组值，
// 界面按 status=error 查失败时不会把事件混进来，又能专门筛出来回答
// "这把密钥为什么不转了"。
const (
	logEventKeyCooldown  = "KEY_COOLDOWN"
	logEventKeyDisabled  = "KEY_DISABLED"
	logEventKeyRecovered = "KEY_RECOVERED"
	logEventNoUsableKey  = "NO_USABLE_KEY"
)

// handleKeyEvent 是连接池的回调入口：只入队，绝不阻塞调用路径。
// 写库在 keyEventWorker 里做（M9）——这里同步写库的话，DB 抖动会给
// 正在返回的调用最多加 5 秒延迟。队列满时丢弃并记日志：密钥的持久状态
// 有另一份真相（下次 Sync 会重灌），丢一条事件的代价远小于把调用卡住。
func (s *Service) handleKeyEvent(ev pool.KeyEvent) {
	select {
	case s.keyEventCh <- ev:
	default:
		s.log.Error("密钥事件队列已满，丢弃本条事件",
			zap.String("server", ev.ServerKey), zap.String("credential", ev.KeyLabel),
			zap.String("verdict", string(ev.Verdict)), zap.String("detail", ev.Detail))
	}
}

// keyEventWorker 逐条消费密钥事件。单消费者保序：同一把密钥的
// "冷却 → 弃用 → 恢复"不会在库里乱序。Close 时先排空残余再退出，
// 进程收尾前的最后几条状态变化也要落库。
func (s *Service) keyEventWorker() {
	defer s.closeWG.Done()
	for {
		select {
		case <-s.closeCh:
			for {
				select {
				case ev := <-s.keyEventCh:
					s.processKeyEvent(ev)
				default:
					return
				}
			}
		case ev := <-s.keyEventCh:
			s.processKeyEvent(ev)
		}
	}
}

// processKeyEvent 落库密钥状态变化并记一条日志。
func (s *Service) processKeyEvent(ev pool.KeyEvent) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	event := ""
	switch {
	case ev.Disabled:
		event = logEventKeyDisabled
		if err := s.store.Credentials.Disable(ctx, ev.KeyID, ev.Verdict, ev.Detail); err != nil {
			s.log.Error("弃用密钥写入失败",
				zap.String("server", ev.ServerKey), zap.String("credential", ev.KeyLabel), zap.Error(err))
		}
		// 冷却不用另行清理：Disable 一并写掉了（弃用是更重的判决，
		// 留着冷却会让重启后把一个已弃用的密钥提前放回轮换）。
		s.log.Warn("密钥已弃用",
			zap.String("server", ev.ServerKey), zap.String("credential", ev.KeyLabel),
			zap.String("verdict", string(ev.Verdict)), zap.String("detail", ev.Detail))

	case ev.CollectiveProtection:
		// 本会被弃用，但留着它总比一把不剩强。把 server 标为降级，
		// 让用户在概览里就看到"这里有事"，而不是等某次调用失败才知道。
		event = logEventNoUsableKey
		if err := s.store.Servers.MarkNoUsableKey(ctx, ev.ServerID, ev.Detail); err != nil {
			s.log.Error("标记 server 降级失败", zap.String("server", ev.ServerKey), zap.Error(err))
		}
		s.invalidateServers(ctx)
		s.log.Warn("密钥弃用被集体失败保护拦下",
			zap.String("server", ev.ServerKey), zap.String("credential", ev.KeyLabel), zap.String("detail", ev.Detail))

	case ev.Recovered:
		event = logEventKeyRecovered
		// O4：冷却结束要清掉落库的时刻，否则下次启动会把它当成还在冷却。
		if err := s.store.Credentials.SetCoolingUntil(ctx, ev.KeyID, time.Time{}); err != nil {
			s.log.Error("清理密钥冷却失败",
				zap.String("server", ev.ServerKey), zap.String("credential", ev.KeyLabel), zap.Error(err))
		}
		s.log.Info("密钥已从冷却中恢复",
			zap.String("server", ev.ServerKey), zap.String("credential", ev.KeyLabel), zap.String("detail", ev.Detail))

	case ev.Cooling > 0:
		event = logEventKeyCooldown
		// O4：冷却落库，供重启后恢复。写失败只记日志——它是"下次启动的
		// 提示"，丢一次最坏的结果是重启后多挨一次 429，不值得为它报错。
		if err := s.store.Credentials.SetCoolingUntil(ctx, ev.KeyID, ev.CoolingUntil); err != nil {
			s.log.Error("记录密钥冷却失败",
				zap.String("server", ev.ServerKey), zap.String("credential", ev.KeyLabel), zap.Error(err))
		}
		s.log.Info("密钥进入冷却",
			zap.String("server", ev.ServerKey), zap.String("credential", ev.KeyLabel),
			zap.Duration("cooldown", ev.Cooling.Round(time.Second)), zap.String("detail", ev.Detail))
	}

	if event == "" {
		return
	}
	s.enqueueLog(aggregate.LogRecord{
		ServerID:     ev.ServerID,
		ServerKey:    ev.ServerKey,
		Status:       "key",
		Kind:         downstream.ErrorKind(event),
		Err:          ev.Detail,
		CredentialID: ev.KeyID,
	})
}
