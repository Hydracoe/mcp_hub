package hub

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"time"

	"github.com/hydra/mcp-hub/internal/downstream"
	"github.com/hydra/mcp-hub/internal/hub/retrieval"
	"github.com/hydra/mcp-hub/internal/store/servers"
	"github.com/hydra/mcp-hub/internal/store/storeutil"
	"go.uber.org/zap"
)

// RefreshReport 是一次全量刷新的结果。
//
// Total 是参与探活的数量（即已启用的 server），停用项单独计入 Disabled，
// 因此恒有 Healthy + Failed == Total。把停用项混进 Total 会让"主动关掉的"
// 与"漏掉的"看起来一样，健康报告就失去意义。
type RefreshReport struct {
	Total    int           `json:"total"`
	Healthy  int           `json:"healthy"`
	Failed   int           `json:"failed"`
	Disabled int           `json:"disabled"`
	ToolNum  int           `json:"toolNum"`
	Elapsed  time.Duration `json:"elapsed"`
	Results  []ServerProbe `json:"results"`
}

// ServerProbe 是单个 server 的探活摘要。
type ServerProbe struct {
	ServerID  string               `json:"serverId"`
	ServerKey string               `json:"serverKey"`
	OK        bool                 `json:"ok"`
	Kind      downstream.ErrorKind `json:"kind,omitempty"`
	Detail    string               `json:"detail,omitempty"`
	ToolCount int                  `json:"toolCount"`
	LatencyMS int64                `json:"latencyMs"`
}

// RefreshAll 并发探活全部已启用的 server，然后把结果落库并重建聚合快照。
// 这里是故障隔离的关键：每个 server 在独立 goroutine 里带独立超时探活，
// 任何一个失败都只写它自己的状态行，不会让整个刷新返回错误。
func (s *Service) RefreshAll(ctx context.Context) *RefreshReport {
	started := time.Now()

	srvs, err := s.store.Servers.List(ctx)
	if err != nil {
		s.log.Error("刷新前读取 server 列表失败", zap.Error(err))
		return &RefreshReport{Elapsed: time.Since(started)}
	}

	// 先把停用项摘出去：它们不探活、不进结果、也不计入成败——
	// 否则"停用"会被报告成"探活失败"，Healthy + Failed 还会大于 Total。
	targets := make([]servers.Server, 0, len(srvs))
	for _, sv := range srvs {
		if sv.Enabled {
			targets = append(targets, sv)
		}
	}

	report := &RefreshReport{
		Total:    len(targets),
		Disabled: len(srvs) - len(targets),
	}
	// 与 targets 按下标对齐：每个槽位都由对应的 goroutine 写入，
	// 因此 wg.Wait 之后不存在未被填充的零值项。
	results := make([]ServerProbe, len(targets))

	sem := make(chan struct{}, probeConcurrency)
	var wg sync.WaitGroup

	for i, sv := range targets {
		wg.Add(1)
		go func(idx int, sv servers.Server) {
			defer wg.Done()

			sem <- struct{}{}
			defer func() { <-sem }()

			res := downstream.NewProber(sv.Spec).Probe(ctx)

			// 探活失败不清空工具缓存（由 SaveProbe 保证）：
			// 下游抖动不该让已聚合的工具消失。
			stats, err := s.store.Servers.SaveProbeReported(ctx, sv.Spec.ID, res)
			if err != nil {
				s.log.Error("写入探活结果失败",
					zap.String("server", sv.Spec.Key), zap.Error(err))
			} else if stats.Total() > 0 {
				s.log.Info("下游工具已更新",
					zap.String("server", sv.Spec.Key),
					zap.Int("added", stats.Added),
					zap.Int("updated", stats.Updated),
					zap.Int("removed", stats.Removed),
					zap.Int("autoEnabled", stats.AutoEnabled))
			}
			s.noteGate(sv.Spec.Key, sv.Spec.ID, res)

			results[idx] = ServerProbe{
				ServerID:  sv.Spec.ID,
				ServerKey: sv.Spec.Key,
				OK:        res.OK,
				Kind:      res.Kind,
				Detail:    res.Detail,
				ToolCount: res.ToolCount,
				LatencyMS: res.Latency.Total,
			}

			if !res.OK {
				s.log.Warn("下游探活失败",
					zap.String("server", sv.Spec.Key),
					zap.String("kind", string(res.Kind)),
					zap.String("detail", res.Detail))
			} else {
				// 成功走 debug：45 秒一轮的周期探活，全打 info 会把
				// 真正值得看的事件淹没掉。
				s.log.Debug("探活成功",
					zap.String("server", sv.Spec.Key),
					zap.Int("tools", res.ToolCount),
					zap.Int64("ms", res.Latency.Total))
			}
		}(i, sv)
	}

	wg.Wait()

	// 重新装载聚合快照。用一份新的上下文：单个探活的失败与超时
	// 不该把这一步也一起取消掉。
	s.refreshAgg()
	s.invalidateServers(ctx)

	for _, r := range results {
		if r.OK {
			report.Healthy++
		} else {
			report.Failed++
		}
	}
	report.Results = results
	report.ToolNum = s.reg.Current().ToolCount()
	report.Elapsed = time.Since(started)

	return report
}

// ProbeServer 探活单个 server 并落库，但不动聚合快照。
// 供管理界面的"测试连通性"用：测试只回答"这个配置能不能用"，
// 要不要影响线上工具列表由用户决定。
func (s *Service) ProbeServer(ctx context.Context, serverID string) (*downstream.ProbeResult, error) {
	sv, err := s.store.Servers.Get(ctx, serverID)
	if err != nil {
		return nil, err
	}

	res := downstream.NewProber(sv.Spec).Probe(ctx)
	stats, err := s.store.Servers.SaveProbeReported(ctx, serverID, res)
	if err != nil {
		return nil, err
	}
	s.noteGate(sv.Spec.Key, sv.Spec.ID, res)
	if stats.Total() > 0 {
		s.log.Info("下游工具已更新",
			zap.String("server", sv.Spec.Key),
			zap.Int("added", stats.Added),
			zap.Int("updated", stats.Updated),
			zap.Int("removed", stats.Removed),
			zap.Int("autoEnabled", stats.AutoEnabled))
	}
	if !res.OK {
		s.log.Warn("单点探活失败",
			zap.String("server", sv.Spec.Key),
			zap.String("kind", string(res.Kind)),
			zap.String("detail", res.Detail))
	} else {
		s.log.Debug("单点探活成功",
			zap.String("server", sv.Spec.Key),
			zap.Int("tools", res.ToolCount),
			zap.Int64("ms", res.Latency.Total))
	}
	// SaveProbe 会改 status 与探活结果，界面上的那两栏要立刻跟着变。
	s.invalidateServers(ctx)
	return res, nil
}

// ApplyResult 是 ApplyServer 的结果。
// SuggestedCategories 只在"新建 + 未指定分类 + 嵌入已启用"时有值：
// top-1 过阈值则分类已自动应用，其余候选供界面让用户选择。
type ApplyResult struct {
	ID                  string                        `json:"id"`
	Created             bool                          `json:"created"`
	SuggestedCategories []retrieval.SuggestedCategory `json:"suggestedCategories,omitempty"`
}

// CategoryRequiredError 表示新建 server 时缺少分类且无法自动补齐。
// 分类是必填项（组织单元、检索过滤与 facet 的来源），自动归类只是补齐
// 手段：没过阈值时用户必须选一个或先建分类，所以错误带着候选一起返回。
type CategoryRequiredError struct {
	Suggested []retrieval.SuggestedCategory
}

func (e *CategoryRequiredError) Error() string {
	if len(e.Suggested) > 0 {
		return "新建 server 必须指定分类（自动归类未达阈值，请从候选中选择）"
	}
	return "新建 server 必须指定分类"
}

// CategoryNotFoundError 表示引用的分类不存在。
//
// 分类是 server 的单值归属：写进去一个不存在的 id 就是悬空引用——
// 界面上显示成一行空白分类，检索侧则永远查不到它。新建与更新两条路
// 都要拦，拦在落库之前，不留脏行。
type CategoryNotFoundError struct {
	ID string
}

func (e *CategoryNotFoundError) Error() string {
	return fmt.Sprintf("分类 %q 不存在", e.ID)
}

// findExisting 找出这次写入对应的已有记录（没有则 found=false）。
//
// 判据是"这条记录在不在"，不是"这个 key 在不在"：更新路径带着已知的
// id（改了 key 也是原地更新，同一行换前缀），按 key 查会查不到，于是
// 一次更新被误判成新建——新建要校验分类，于是"改个 key"就被"必须
// 指定分类"挡住，返回的 Created 标志也成了错的。
//
// id 为空的创建路径按 key 判重：POST ?upsert=1 覆盖已有 key 时它已有
// 记录，同样不是新建。
func (s *Service) findExisting(ctx context.Context, cfg servers.Config) (servers.Server, bool, error) {
	var (
		sv  servers.Server
		err error
	)
	if cfg.Spec.ID != "" {
		sv, err = s.store.Servers.Get(ctx, cfg.Spec.ID)
	} else {
		sv, err = s.store.Servers.GetByKey(ctx, cfg.Spec.Key)
	}
	switch {
	case err == nil:
		return sv, true, nil
	case errors.Is(err, storeutil.ErrNotFound):
		return servers.Server{}, false, nil
	default:
		return servers.Server{}, false, err
	}
}

// isNewServer 判断这次写入是"新建"还是"更新已有记录"。
func (s *Service) isNewServer(ctx context.Context, cfg servers.Config) (bool, error) {
	_, found, err := s.findExisting(ctx, cfg)
	if err != nil {
		return false, err
	}
	return !found, nil
}

// checkCategoryExists 校验分类真实存在。
//
// 不存在时报领域错误（CategoryNotFoundError），让上层翻译成 400 而不是
// 500：写一个不存在的分类是请求写错了，不是服务端出故障。
func (s *Service) checkCategoryExists(ctx context.Context, id string) error {
	if _, err := s.store.Categories.Get(ctx, id); err != nil {
		if errors.Is(err, storeutil.ErrNotFound) {
			return &CategoryNotFoundError{ID: id}
		}
		return err
	}
	return nil
}

// ApplyServer 写入一个新 server 或更新已有配置，并让它立即生效。
//
// 要不要重建连接取决于目标是否换了，判据是 ConnFingerprint：换密钥、
// 改过滤名单、改显示名都不动连接，只有 url / command / env / 超时 /
// 协议版本变了才重建——拆连接会打断正在跑的调用，而用户点"改个名字"
// 或"轮换密钥"时并没有这个预期。
//
// 新建时分类必填（PRD §4.7.3）：显式指定则校验存在性；未指定且嵌入已
// 启用则先跑自动归类，top-1 过阈值即应用；都落空则返回
// CategoryRequiredError（带候选），不落库。
//
// 更新时分类同样不能是悬空引用：显式给出就必须真实存在，否则报
// CategoryNotFoundError，不落库。没带分类（CategoryID 为空且没有
// ClearCategory）则沿用原分类——把"没提"当成"清空"会在每次没带该
// 字段的保存里悄悄抹掉分类。
//
// 改 key 仍是更新：改的是同一行的前缀，不是新建一条，所以不走上面
// 那套新建校验（判据见 isNewServer）。
func (s *Service) ApplyServer(ctx context.Context, cfg servers.Config) (*ApplyResult, error) {
	created, err := s.isNewServer(ctx, cfg)
	if err != nil {
		return nil, err
	}

	// 分类校验在 upsert 之前做：落库后才发现缺分类，要么删行回滚
	// （多一次写、一个竞态窗口），要么留下脏行。
	var suggestions []retrieval.SuggestedCategory
	if created {
		if strings.TrimSpace(cfg.CategoryID) == "" {
			if em := s.retr.Embedder(); em != nil {
				text := cfg.Description
				if text == "" {
					text = cfg.Spec.Name
				}
				applied, sug, err := em.AutoCategorize(ctx, text)
				if err != nil {
					s.log.Warn("自动归类失败，需手动指定",
						zap.String("server", cfg.Spec.Key), zap.Error(err))
				} else {
					suggestions = sug
					if applied != nil {
						cfg.CategoryID = applied.ID
						s.log.Info("server 将自动归类",
							zap.String("server", cfg.Spec.Key),
							zap.String("category", applied.Name))
					}
				}
			}
			if strings.TrimSpace(cfg.CategoryID) == "" {
				// 嵌入未启用或自动归类没过阈值：分类无法自动补齐，拒绝
				// 创建。存量 server 不受影响（可为未分类）。
				return nil, &CategoryRequiredError{Suggested: suggestions}
			}
		}
		if err := s.checkCategoryExists(ctx, cfg.CategoryID); err != nil {
			return nil, err
		}
	} else {
		switch {
		case cfg.ClearCategory:
			// 明写要清空：退回"未分类"。
			cfg.CategoryID = ""
		case strings.TrimSpace(cfg.CategoryID) == "":
			// 这次没提分类：沿用原分类。原分类可能是空的（存量未分类的
			// server），那就还是空——不因为"没提"就凭空造一个分类出来。
			prev, found, err := s.findExisting(ctx, cfg)
			if err != nil {
				return nil, err
			}
			if found {
				cfg.CategoryID = prev.CategoryID
			}
		default:
			// 显式给了分类：必须真实存在，否则写下去的是悬空引用。
			if err := s.checkCategoryExists(ctx, cfg.CategoryID); err != nil {
				return nil, err
			}
		}
	}

	if err := s.store.Servers.Upsert(ctx, cfg); err != nil {
		return nil, err
	}

	saved, err := s.store.Servers.GetByKey(ctx, cfg.Spec.Key)
	if err != nil {
		return nil, err
	}

	// 编辑表单也能把 server 关掉（cfg.Enabled=false）：与启停开关同一约束——
	// 停用的 server 下不允许有启用的工具，这里补一次级联（幂等，
	// 本来就全禁用时什么都不发生）。
	if !saved.Enabled {
		flipped, err := s.store.Servers.DisableToolsOfServer(ctx, saved.Spec.ID)
		if err != nil {
			return nil, err
		}
		s.recordCascadeDisable(ctx, saved.Spec.ID, saved.Spec.Key, flipped)
	}

	// 凭据热更新：把最新一份交给现有连接，下一个请求就用它，连接不动、
	// 在飞调用不受影响——轮换密钥对调用方应当是隐形的。若这次同时动了
	// 目标，下面的 Reload 会让这条连接退场，热更新白做一次，无害。
	if s.pool.UpdateCredentials(saved.Spec.ID, saved.Spec.Headers) {
		s.log.Info("已热更新下游凭据", zap.String("server", saved.Spec.Key))
	}

	// 落库之后刷新运行时配置。刷新失败不是这次写的失败：配置已经在
	// 库里了（失败只记日志），所以下面照常失效缓存——跳过它会让界面
	// 一直显示旧配置，下次读还会命中旧值。
	s.reloadAfterCommit(ctx, saved.Spec.Key, saved.Spec.ID)
	s.invalidateServers(ctx)
	result := &ApplyResult{ID: saved.Spec.ID, Created: created}
	// 自动归类的 top-3 候选随结果返回（自动应用成功时也返回，
	// 让界面能展示"已自动归到 X，也可改选 Y"）。
	result.SuggestedCategories = suggestions
	if created {
		s.log.Info("server 已创建",
			zap.String("server", saved.Spec.Key),
			zap.String("transport", string(saved.Spec.Transport)),
			zap.Bool("enabled", saved.Enabled))
	} else {
		s.log.Info("server 配置已更新",
			zap.String("server", saved.Spec.Key),
			zap.String("transport", string(saved.Spec.Transport)))
	}

	return result, nil
}

// RefreshTools 探活单个 server，刷新它的工具缓存并重建聚合快照。
// 与 ProbeServer 的区别就在最后一步：点"刷新工具"时用户期待的是工具
// 列表真的更新，而不是只刷新状态角标。
func (s *Service) RefreshTools(ctx context.Context, serverID string) (*downstream.ProbeResult, error) {
	res, err := s.ProbeServer(ctx, serverID)
	if err != nil {
		return nil, err
	}
	s.refreshAgg()
	return res, nil
}

// noteGate 把探活结果交给连败拦截闸，并在状态翻转时打点。
// 所有产生落库探活结果的路径（周期调度 / 全量刷新 / 手动单点）都该调用，
// 这样手动点一次"测试连通性"成功也能解封。
func (s *Service) noteGate(serverKey, serverID string, res *downstream.ProbeResult) {
	ev := s.gate.note(serverID, res.OK, res.Detail)
	switch {
	case ev.JustBlocked:
		s.log.Warn("连续探活失败，已暂停该上游的工具调用（探活继续，恢复后自动解除）",
			zap.String("server", serverKey),
			zap.Int("streak", ev.Streak),
			zap.String("reason", ev.Reason))
	case ev.Recovered:
		s.log.Info("上游探活恢复，解除调用暂停",
			zap.String("server", serverKey))
	}
}

// ─── 错峰探活调度 ────────────────────────────────────────────────

// probeTick 是错峰调度器的节拍。
const probeTick = time.Second

// probeBackoffShiftCap 限制连续失败时退避倍数的位移上限：倍数 = 2^shift，
// 封顶 8 倍周期。下游挂了不该被每 45s 敲一次门，但也别退到天荒地老——
// 它恢复后最多一个多小时就会被重新探到。
const probeBackoffShiftCap = 3

// probeSlot 是一个 server 在调度器里的状态。
type probeSlot struct {
	nextAt time.Time // 下次探活时刻
	fails  int       // 连续失败次数（驱动退避）
}

// StartProbeScheduler 启动错峰探活循环。ctx 取消（进程退出）即停止。
//
// 与 RefreshAll 的分工：这里负责无人值守的周期探活，RefreshAll 只服务
// 管理界面的手动刷新（语义是"现在立刻全部探一遍"）。
func (s *Service) StartProbeScheduler(ctx context.Context, interval time.Duration) {
	s.closeWG.Add(1)
	go func() {
		defer s.closeWG.Done()
		s.probeScheduler(ctx, interval)
	}()
}

// probeScheduler 把"每个周期全量探一遍"改成"每个 server 在周期内随机
// 相位独立探活"。
//
// 几十个 server、几千个工具时，同一时刻至多只有个位数请求在飞，且任意
// 两个 server 的探活时刻互不相同——下游看到的是匀速滴流而不是周期性的
// 并发脉冲。随机相位在 server 首次进入调度时分配一次并长期保持。
func (s *Service) probeScheduler(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(probeTick)
	defer ticker.Stop()

	slots := make(map[string]*probeSlot)

	for {
		select {
		case <-ctx.Done():
			return
		case <-s.closeCh:
			return
		case now := <-ticker.C:
			s.scheduleProbes(ctx, now, interval, slots)
		}
	}
}

// scheduleProbes 对齐调度状态与库中的 enabled server 集合，并把到期的
// 探活发出去。
func (s *Service) scheduleProbes(ctx context.Context, now time.Time, interval time.Duration, slots map[string]*probeSlot) {
	srvs, err := s.store.Servers.List(ctx)
	if err != nil {
		// 读库失败只跳过本节拍：下一秒再来，不影响任何在飞探活。
		s.log.Error("错峰探活读取 server 列表失败", zap.Error(err))
		return
	}

	live := make(map[string]servers.Server, len(srvs))
	for _, sv := range srvs {
		if sv.Enabled {
			live[sv.Spec.ID] = sv
		}
	}

	// 已删除或停用的 server 摘出调度（停用后不再产生任何探活流量）。
	for id := range slots {
		if _, ok := live[id]; !ok {
			delete(slots, id)
		}
	}

	for id, sv := range live {
		slot, ok := slots[id]
		if !ok {
			// 新面孔：相位在周期内随机。错峰的关键就是这一步——
			// 哪怕一百个 server 同一分钟创建，它们的探活时刻也会
			// 均匀撒在整个周期上。
			slots[id] = &probeSlot{nextAt: now.Add(time.Duration(rand.Int63n(int64(interval))))}
			continue
		}
		if slot.nextAt.IsZero() || now.Before(slot.nextAt) {
			continue
		}

		// 到期。inFlight 由 probeOne 自己清，不在这里并发启动第二个。
		slot.nextAt = time.Time{}
		go s.probeOne(ctx, sv, slot, interval)
	}
}

// probeOne 探活单个 server：落库对账、打变更日志、按结果安排下一次。
// 阻塞式占一个全局探活名额：极端情况下（大量 server 同时恢复）也只是
// 在这里排队，不会压向下游。
func (s *Service) probeOne(ctx context.Context, sv servers.Server, slot *probeSlot, interval time.Duration) {
	s.probeSem <- struct{}{}
	defer func() { <-s.probeSem }()

	res := downstream.NewProber(sv.Spec).Probe(ctx)

	stats, err := s.store.Servers.SaveProbeReported(ctx, sv.Spec.ID, res)
	if err != nil {
		s.log.Error("写入探活结果失败", zap.String("server", sv.Spec.Key), zap.Error(err))
	}
	s.noteGate(sv.Spec.Key, sv.Spec.ID, res)

	switch {
	case !res.OK:
		slot.fails++
		// 指数退避：2 → 4 → 8 倍周期封顶。下游挂了不该每 45s 敲门，
		// 但也必须保持试探——它恢复后最多 8 个周期内自动归队。
		backoff := interval * time.Duration(1<<min(slot.fails, probeBackoffShiftCap))
		slot.nextAt = time.Now().Add(backoff)
		s.log.Warn("周期探活失败，已安排退避重试",
			zap.String("server", sv.Spec.Key),
			zap.String("kind", string(res.Kind)),
			zap.String("detail", res.Detail),
			zap.Duration("retryIn", backoff.Round(time.Second)))
	default:
		slot.fails = 0
		slot.nextAt = time.Now().Add(interval)
		if stats.Total() > 0 {
			// 上游迭代了什么，日志里一眼可见；明细在 tool_changes。
			// autoEnabled 是 added 里被自动放行的一部分：它不进"待人工
			// 处理"，但必须留痕，否则"这个工具谁批准上线的"无从回答。
			s.log.Info("下游工具已更新",
				zap.String("server", sv.Spec.Key),
				zap.Int("added", stats.Added),
				zap.Int("updated", stats.Updated),
				zap.Int("removed", stats.Removed),
				zap.Int("autoEnabled", stats.AutoEnabled))
		} else {
			s.log.Debug("周期探活成功",
				zap.String("server", sv.Spec.Key),
				zap.Int("tools", res.ToolCount),
				zap.Int64("ms", res.Latency.Total))
		}
		s.refreshAggThrottled()
	}
}
