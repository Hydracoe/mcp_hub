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
// name 是中文展示名，slug 是英文名，两者都只是展示/检索元数据，
// 不进对外工具名。slug 的格式校验在 store 层，这里只管透传。
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
//
// 分类的任何变化都不改对外工具名，所以这里不广播 tools/list_changed、
// 也不触发向量重建：工具名不含分类 → 快照指纹不变；分类不在向量语料里
// → 语料哈希不变，一次 embedding 调用都不会多发。
//
// 唯一要做的是重建内存里的检索索引：分类名仍参与 BM25 打分
// 与 facet，改完必须立刻生效，不能等到下一轮探活。纯内存操作，零 API 成本。
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
		// 只有展示名/描述会影响检索文本；改 slug 只影响分类自己的标识，
		// 连内存索引都不用重建。
		s.refreshAgg()
	}
	return &cat, nil
}

// DeleteCategory 删除分类。
// 分类下还有 server 时 store 层拒绝（ErrCategoryInUse → 409）：删分类
// 不顺带把 server 摘成未分类，那是一次用户没点过的批量改动。
// 先读一次名字只为日志——删完行就没了，日志里只剩 id 认不出是谁。
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
//
// 直接读库而不是读聚合快照：快照只含已启用的 server，而管理界面必须
// 能看到被禁用的那些，否则用户停用后就再也找不到它了。结果走缓存，
// 所有写路径都会同步删掉它，所以"改完立刻读回来"拿到的仍是新值。
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
// 必须由写路径同步调用，不能只靠 TTL 过期：缓存晚几秒在用户看来
// 就是"这次没存上"。
func (s *Service) invalidateServers(ctx context.Context) {
	// 脱离取消：写操作已经提交了，这时候请求上下文可能已经被取消，
	// 而缓存删不掉会留下一个过期视图。
	ctx = context.WithoutCancel(ctx)
	if err := s.servers.InvalidateAll(ctx); err != nil {
		s.log.Warn("清理 server 列表缓存失败", zap.Error(err))
	}
}

// RemoveServer 删除一个 server 并让聚合列表立即反映这一变化。
// 删记录是立刻的（工具马上从聚合列表消失），连接则优雅退场——
// 已经跑起来的调用不会因为运维删了一行配置被打断。
func (s *Service) RemoveServer(ctx context.Context, serverID string) error {
	key := s.serverKey(ctx, serverID)
	if err := s.store.Servers.Delete(ctx, serverID); err != nil {
		return err
	}
	s.pool.Retire(serverID)
	// 删除已经提交：刷新失败只是运行时状态没跟上（记日志），
	// 缓存的失效照常执行，否则列表里会一直留着这个已删的 server。
	s.reloadAfterCommit(ctx, key, serverID)
	s.invalidateServers(ctx)
	s.log.Info("server 已删除", zap.String("server", key))
	return nil
}

// SetEnabled 启停一个 server。停用时级联禁用它的全部工具（约束见
// SetEnabledCascade），重启用不恢复工具——哪些工具该回来由人重新决定。
//
// 停用分两步、顺序不能反：先把工具从聚合列表摘掉（AI 从此不会再派新
// 调用），再让连接优雅退场（在飞的跑完才关）。这才是用户点"停用"想要
// 的语义——别再被调用了，而不是把正在做的事拦腰截断。
func (s *Service) SetEnabled(ctx context.Context, serverID string, enabled bool) error {
	key := s.serverKey(ctx, serverID)
	flipped, err := s.store.Servers.SetEnabledCascade(ctx, serverID, enabled)
	if err != nil {
		return err
	}
	if !enabled {
		s.pool.Retire(serverID)
		// 密钥池与连接同一步退场：停用的 server 不该继续持有可用密钥，
		// 它们随时可能被选中发出去。这一步不等 Reload——刷新失败时
		// 运行时状态会滞后，而"停用"的语义是"别再用它"。
		s.pool.DropKeyRing(serverID)
		s.recordCascadeDisable(ctx, serverID, key, flipped)
	}
	// 启停已经提交：刷新失败只是运行时状态没跟上（记日志），
	// 缓存的失效照常执行，否则界面会一直显示旧状态。
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
// 停用不是瞬时动作：当时有调用在跑，连接就要等它们结束。管理界面据此
// 显示"停用中 · 还有 N 个调用在收尾"。
func (s *Service) DrainingInfo(serverID string) (pool.DrainingInfo, bool) {
	return s.pool.DrainingOf(serverID)
}
