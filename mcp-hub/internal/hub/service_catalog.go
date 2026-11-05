package hub

import (
	"context"
	"time"

	"github.com/hydra/mcp-hub/internal/hub/pool"
	"github.com/hydra/mcp-hub/internal/store/categories"
	"github.com/hydra/mcp-hub/internal/store/logs"
	"github.com/hydra/mcp-hub/internal/store/servers"
	"go.uber.org/zap"
)

// Categories 返回分类列表（含每个分类的 server 数量）。
func (s *Service) Categories(ctx context.Context) ([]categories.Category, error) {
	return s.store.Categories.List(ctx)
}

// CreateCategory 新建一个分类。
// name 与 slug 只是展示/检索元数据，不进对外工具名；slug 格式校验在 store 层。
func (s *Service) CreateCategory(ctx context.Context, name, slug, description string) (*categories.Category, error) {
	cat, err := s.store.Categories.Create(ctx, name, slug, description)
	if err != nil {
		return nil, err
	}
	s.log.Info("分类已创建",
		zap.String("category", cat.Name), zap.String("slug", cat.Slug))
	return &cat, nil
}

// UpdateCategory 更新分类的中文名、英文名或描述。
// 分类变化不改对外工具名，因此不广播 tools/list_changed、不触发向量重建；
// 只需重建内存检索索引，让分类名参与的 BM25 打分与 facet 立刻生效。
func (s *Service) UpdateCategory(ctx context.Context, id, name, slug, description string) (*categories.Category, error) {
	before, err := s.store.Categories.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	cat, err := s.store.Categories.Update(ctx, id, name, slug, description)
	if err != nil {
		return nil, err
	}
	s.log.Info("分类已更新",
		zap.String("category", cat.Name), zap.String("slug", cat.Slug))
	if before.Name != cat.Name || before.Description != cat.Description {
		// 只有展示名/描述会影响检索文本；改 slug 无需重建索引。
		s.refreshAgg()
	}
	return &cat, nil
}

// DeleteCategory 删除分类。分类下还有 server 时 store 层拒绝（ErrCategoryInUse → 409），
// 不会顺带把 server 摘成未分类。先读名字只为日志：删完只剩 id 认不出是谁。
func (s *Service) DeleteCategory(ctx context.Context, id string) error {
	name := ""
	if cat, err := s.store.Categories.Get(ctx, id); err == nil {
		name = cat.Name
	}
	if err := s.store.Categories.Delete(ctx, id); err != nil {
		return err
	}
	s.log.Info("分类已删除", zap.String("category", name), zap.String("id", id))
	s.invalidateServers(ctx)
	s.refreshAgg()
	return nil
}

// Servers 返回全部 server 的当前状态（含最近一次探活结果）。
// 直接读库而非聚合快照：快照只含已启用的 server，管理界面必须能看到被禁用的。
// 结果走缓存，写路径会同步删除，所以"改完立刻读回来"拿到的仍是新值。
func (s *Service) Servers(ctx context.Context) ([]servers.Server, error) {
	return s.servers.GetOrLoad(ctx, serversKey, serversTTL, s.store.Servers.List)
}

// RecentStats 返回最近 window 时间窗内的调用统计。
func (s *Service) RecentStats(ctx context.Context, window time.Duration) (logs.CallStats, error) {
	return s.stats.GetOrLoad(ctx, window.String(), statsTTL,
		func(ctx context.Context) (logs.CallStats, error) {
			return s.store.Logs.Stats(ctx, time.Now().Add(-window))
		})
}

// invalidateServers 让 server 列表缓存立即失效。
// 必须由写路径同步调用：缓存晚几秒在用户看来就是"这次没存上"。
func (s *Service) invalidateServers(ctx context.Context) {
	// 脱离取消：写操作已经提交，此时请求上下文可能已被取消，
	// 缓存删不掉会留下过期视图。
	ctx = context.WithoutCancel(ctx)
	if err := s.servers.InvalidateAll(ctx); err != nil {
		s.log.Warn("清理 server 列表缓存失败", zap.Error(err))
	}
}

// RemoveServer 删除一个 server 并让聚合列表立即反映这一变化。
// 删记录是立刻的，连接则优雅退场，已开始的调用不会被打断。
func (s *Service) RemoveServer(ctx context.Context, serverID string) error {
	key := s.serverKey(ctx, serverID)
	if err := s.store.Servers.Delete(ctx, serverID); err != nil {
		return err
	}
	s.pool.Retire(serverID)
	// 删除已提交：刷新失败只是运行时状态没跟上（记日志），
	// 缓存失效照常执行，否则列表会一直留着这个已删的 server。
	s.reloadAfterCommit(ctx, key, serverID)
	s.invalidateServers(ctx)
	s.log.Info("server 已删除", zap.String("server", key))
	return nil
}

// SetEnabled 启停一个 server。停用时级联禁用全部工具（约束见 SetEnabledCascade），重启用不恢复工具。
// 停用顺序不能反：先把工具从聚合列表摘掉（AI 不再派新调用），
// 再让连接优雅退场（在飞的跑完才关）。
func (s *Service) SetEnabled(ctx context.Context, serverID string, enabled bool) error {
	key := s.serverKey(ctx, serverID)
	flipped, err := s.store.Servers.SetEnabledCascade(ctx, serverID, enabled)
	if err != nil {
		return err
	}
	if !enabled {
		s.pool.Retire(serverID)
		// 密钥池与连接同一步退场：停用的 server 不该继续持有可用密钥。
		// 这一步不等 Reload——"停用"的语义就是"别再用它"。
		s.pool.DropKeyRing(serverID)
		s.recordCascadeDisable(ctx, serverID, key, flipped)
	}
	// 启停已提交：刷新失败只是运行时状态没跟上（记日志），
	// 缓存失效照常执行，否则界面会一直显示旧状态。
	s.reloadAfterCommit(ctx, key, serverID)
	s.invalidateServers(ctx)
	if enabled {
		s.log.Info("server 已启用", zap.String("server", key))
	} else {
		s.log.Info("server 已停用", zap.String("server", key))
	}
	return nil
}

// DrainingInfo 返回某个 server 的停用收尾进度。
// 停用不是瞬时的：有调用在跑就要等它们结束，管理界面据此显示收尾数量。
func (s *Service) DrainingInfo(serverID string) (pool.DrainingInfo, bool) {
	return s.pool.DrainingOf(serverID)
}
