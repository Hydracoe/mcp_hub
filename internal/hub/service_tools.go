package hub

import (
	"context"
	"fmt"
	"time"

	"github.com/hydra/mcp-hub/internal/store/storeutil"
	"github.com/hydra/mcp-hub/internal/store/toolchanges"
	"go.uber.org/zap"
)

// SetToolEnabled 启用或禁用单个工具，并立刻让聚合端点生效。
//
// 启用方向要求所属 server 处于启用状态：停用 server 时它的工具已被级联
// 禁用，放行单个启用会造出「server 停用却有权重工具启用」的矛盾状态——
// 工具显示已启用却对外不可见，排障时只会让人怀疑哪里出了 bug。
//
// 与 server 级启停同一节奏：先落库（含一条变更记录），再重建聚合快照——
// 禁用是"别再被 AI 看到/调到"，必须当场从 tools/list 撤下，而不是等
// 下一轮探活。不存在的工具名返回 storeutil.ErrNotFound（上层 404）。
func (s *Service) SetToolEnabled(ctx context.Context, serverID, toolName string, enabled bool) error {
	key := s.serverKey(ctx, serverID)

	if enabled {
		// 先验工具存在性，再验 server 状态：未知工具永远 404，
		// 不能因为 server 恰好停用就把 404 吃成"先启用 Server"。
		ok, err := s.store.Servers.HasTool(ctx, serverID, toolName)
		if err != nil {
			return err
		}
		if !ok {
			return storeutil.ErrNotFound
		}
		sv, err := s.store.Servers.Get(ctx, serverID)
		if err != nil {
			return err
		}
		if !sv.Enabled {
			return fmt.Errorf("%w（%s）", ErrServerDisabled, key)
		}
	}

	ok, err := s.store.Servers.SetToolEnabled(ctx, serverID, toolName, enabled)
	if err != nil {
		return err
	}
	if !ok {
		// 启用方向返回 false 可能是并发停用 server 让 WHERE 条件落空。
		// 回读一次给出准确原因，别把「server 刚被停用」误报成「工具不存在」。
		if enabled {
			if sv, err := s.store.Servers.Get(ctx, serverID); err == nil && !sv.Enabled {
				return fmt.Errorf("%w（%s）", ErrServerDisabled, key)
			}
		}
		return storeutil.ErrNotFound
	}

	action := toolchanges.ActionEnabled
	if !enabled {
		action = toolchanges.ActionDisabled
	}
	if err := s.store.ToolChanges.Insert(ctx, toolchanges.Change{
		ServerID:  serverID,
		ServerKey: key,
		ToolName:  toolName,
		Action:    action,
		Actor:     toolchanges.ActorAdmin,
		TS:        time.Now(),
	}); err != nil {
		// 留痕失败不回滚启停本身：开关已经生效，只是少了一条审计行；
		// 日志里留痕便于补账。
		s.log.Warn("工具变更记录写入失败",
			zap.String("server", key), zap.String("tool", toolName), zap.Error(err))
	}

	s.refreshAgg()
	s.invalidateServers(ctx)
	s.log.Info("工具启用状态已更新",
		zap.String("server", key), zap.String("tool", toolName), zap.Bool("enabled", enabled))

	// 禁用方向补一条约束：server 没有可用工具就自动停用。否则会留下
	// "启用中却 0 个工具"的空转状态——探活照跑、健康统计照算，但 AI 侧
	// 永远看不到它的任何工具，概览数字只会让人困惑。
	if !enabled {
		s.autoDisableIfNoTools(ctx, serverID, key)
	}
	return nil
}

// autoDisableIfNoTools 在禁用工具后检查：若所属 server 仍启用且启用工具数
// 归零，就把它一并停用（级联禁用是幂等空转，连接优雅退场）。检查或停用
// 失败只打日志不回传：工具停用本身已成功，自动停用是附加动作，下一轮
// 再禁一个工具还会重新触发检查。
func (s *Service) autoDisableIfNoTools(ctx context.Context, serverID, key string) {
	sv, err := s.store.Servers.Get(ctx, serverID)
	if err != nil {
		s.log.Warn("自动停用检查读取 server 失败", zap.String("server", key), zap.Error(err))
		return
	}
	if !sv.Enabled {
		return
	}
	n, err := s.store.Servers.CountEnabledTools(ctx, serverID)
	if err != nil {
		s.log.Warn("自动停用检查统计启用工具失败", zap.String("server", key), zap.Error(err))
		return
	}
	if n > 0 {
		return
	}
	if err := s.SetEnabled(ctx, serverID, false); err != nil {
		s.log.Warn("server 自动停用失败", zap.String("server", key), zap.Error(err))
		return
	}
	s.log.Info("server 已自动停用：启用工具数归零", zap.String("server", key))
}

// recordCascadeDisable 为级联禁用的工具补变更记录。
//
// Actor 用 system 而不是 admin：管理员动的是 server 的开关，并没有对每个
// 工具逐个表态。保留「从未被人工碰过」的判定，server 重新启用后这些工具
// 会重新落进「待启用」告警，提示用户逐个重新决定，而不是悄悄消失。
// 单条写入失败不回滚级联本身：工具已经禁用，只是审计缺一行，日志留痕补账。
func (s *Service) recordCascadeDisable(ctx context.Context, serverID, key string, tools []string) {
	if len(tools) == 0 {
		return
	}
	now := time.Now()
	for _, name := range tools {
		err := s.store.ToolChanges.Insert(ctx, toolchanges.Change{
			ServerID:  serverID,
			ServerKey: key,
			ToolName:  name,
			Action:    toolchanges.ActionDisabled,
			Actor:     toolchanges.ActorSystem,
			Detail:    "Server 停用，工具级联禁用",
			TS:        now,
		})
		if err != nil {
			s.log.Warn("级联禁用变更记录写入失败",
				zap.String("server", key), zap.String("tool", name), zap.Error(err))
		}
	}
	s.log.Info("server 停用，工具已级联禁用",
		zap.String("server", key), zap.Int("count", len(tools)))
}

// ListToolChanges 返回工具变更记录（可按 server 过滤）。
func (s *Service) ListToolChanges(ctx context.Context, q toolchanges.Query) ([]toolchanges.Change, int, error) {
	return s.store.ToolChanges.List(ctx, q)
}

// PendingEnableCount 返回待人工决定的工具数（探活新发现、默认禁用、
// 从未被人工启用或禁用过）。管理界面"待人工处理"告警的数据源。
func (s *Service) PendingEnableCount(ctx context.Context) (int64, error) {
	return s.store.Servers.CountPendingEnable(ctx)
}

// PendingEnableKeys 返回待人工决定的工具集合（serverID + 工具名）。
// 告警卡上的数字与工具页"待启用"筛出来的条数同源，不会对不上。
func (s *Service) PendingEnableKeys(ctx context.Context) (map[string]bool, error) {
	return s.store.Servers.PendingEnableKeys(ctx)
}
