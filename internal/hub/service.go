package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/hydra/mcp-hub/internal/cache"
	"github.com/hydra/mcp-hub/internal/downstream"
	"github.com/hydra/mcp-hub/internal/store"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"go.uber.org/zap"
)

// probeConcurrency 是并发探活的上限。
//
// 不设上限会让 50 个 server 同时建连：对下游是突发流量，
// 对本机是瞬间拉满的 socket。8 是探活这种低频操作的经验值。
const probeConcurrency = 8

// logQueueSize 是调用日志的异步队列长度。
const logQueueSize = 512

// serversTTL 是 server 列表的缓存时长。
//
// 取 2 秒：写路径已经会同步删缓存，这个 TTL 只用来兜住那些
// 没有明确写请求的状态变化（后台周期性探活改的是 status）。
const serversTTL = 2 * time.Second

// statsTTL 是调用统计的缓存时长，比列表长一些。
//
// 它扫的是整张日志表，随日志增长变慢；而日志本身是异步批量落库的，
// 这份数字本来就带滞后，再晚几秒看不出差别。
const statsTTL = 5 * time.Second

// serversKey 是 server 列表缓存里唯一那个 key。
const serversKey = "all"

// Service 把配置、连接、聚合三块编排成一个整体。
//
// 它是网关的中枢：所有会影响"工具长什么样"的操作都从这里过，
// 保证 Registry 与 Pool 不会各自为政。
type Service struct {
	store *store.Store
	pool  *Pool
	reg   *Registry
	agg   *Aggregator
	log   *zap.Logger

	specs   sync.RWMutex
	specMap map[string]downstream.Spec

	// 缓存只挂在"管理界面反复读、且写路径能立刻删掉"的几处。
	// 调用路径（MCP 转发）一次库都不查——工具的聚合结果本来就在
	// Registry 里，所以那里没有缓存可加。
	cache   cache.Store
	servers *cache.Typed[[]store.Server]
	stats   *cache.Typed[store.CallStats]

	// embed 在构造之后由 AttachEmbedding 挂上（handler 依赖 Service 自身，
	// 构造时是环）。HTTP 服务开始接请求前就挂好了，之后只读。
	embed *EmbedManager

	// 检索索引与聚合快照同源，随它一起重建。
	searchMu sync.Mutex
	searchIX *searchIndex

	logCh   chan store.LogEntry
	closeCh chan struct{}
	closeWG sync.WaitGroup

	// last_used_at 的落库节流（见 touchAPIKey）。
	touchMu   sync.Mutex
	touchLast map[string]time.Time
}

// Options 是 NewServiceWithOptions 的可选参数。
type Options struct {
	// Cache 是缓存后端，nil 用进程内实现。
	Cache cache.Store
	// ListMode 控制 AI 客户端 tools/list 看到什么（meta | all）。
	// 库默认 all；CLI 默认 meta（见 cmd/mcp-hub）。
	ListMode string
}

// NewService 构造服务并完成首次聚合装载，缓存用进程内实现。
//
// 首次装载只读已落库的缓存，不触发探活——服务能立刻对外提供
// 上一次已知的工具列表，哪怕此刻所有下游都还没探过。
func NewService(st *store.Store, logger *zap.Logger) *Service {
	return NewServiceWithOptions(st, logger, Options{})
}

// NewServiceWithCache 用指定的缓存后端构造服务。
//
// 换 Redis 时改这里的入参即可，业务代码一行不动。
func NewServiceWithCache(st *store.Store, logger *zap.Logger, c cache.Store) *Service {
	return NewServiceWithOptions(st, logger, Options{Cache: c})
}

// NewServiceWithOptions 是构造服务的全量入口。
func NewServiceWithOptions(st *store.Store, logger *zap.Logger, opts Options) *Service {
	if logger == nil {
		logger = zap.NewNop()
	}
	if opts.Cache == nil {
		opts.Cache = cache.NewMemory()
	}
	if opts.ListMode == "" {
		opts.ListMode = ListModeAll
	}

	s := &Service{
		store:   st,
		pool:    NewPool(logger),
		reg:     NewRegistry(),
		log:     logger,
		specMap: make(map[string]downstream.Spec),
		cache:   opts.Cache,
		servers: cache.NewTyped[[]store.Server](opts.Cache, "hub:servers"),
		stats:   cache.NewTyped[store.CallStats](opts.Cache, "hub:stats"),
		logCh:   make(chan store.LogEntry, logQueueSize),
		closeCh: make(chan struct{}),
	}

	s.agg = NewAggregator(Deps{
		Sources: s.sources,
		SpecOf:  s.specOf,
		Log:     s.enqueueLog,
	}, s.reg, s.pool, opts.ListMode)

	// 密钥状态变化由连接池回调到这里落库与记日志。用回调而不是让
	// 连接池直接写库：连接池不该知道存储的存在。
	s.pool.OnKeyEvent = s.handleKeyEvent

	if err := s.Reload(context.Background()); err != nil {
		s.log.Error("首次装载聚合快照失败", zap.Error(err))
	}

	s.closeWG.Add(1)
	go s.logWorker()

	return s
}

// Aggregator 返回聚合器，供 HTTP 层挂载 MCP 端点。
func (s *Service) Aggregator() *Aggregator { return s.agg }

// Registry 返回聚合路由表。
func (s *Service) Registry() *Registry { return s.reg }

// Pool 返回连接池。
func (s *Service) Pool() *Pool { return s.pool }

// Store 返回持久层。
func (s *Service) Store() *store.Store { return s.store }

// Reload 从存储重新装载连接配置与聚合快照，不触网。
//
// 配置变更后调用它即可让新配置生效，无需重启进程。
func (s *Service) Reload(ctx context.Context) error {
	servers, err := s.store.ListServers(ctx)
	if err != nil {
		return fmt.Errorf("加载 server 配置失败: %w", err)
	}

	specMap := make(map[string]downstream.Spec, len(servers))
	specs := make([]downstream.Spec, 0, len(servers))
	for _, sv := range servers {
		specMap[sv.Spec.ID] = sv.Spec
		specs = append(specs, sv.Spec)
	}

	s.specs.Lock()
	s.specMap = specMap
	s.specs.Unlock()

	// 连接池与配置对齐：已删除的 server 关闭连接，
	// 剩下的保留——重连代价不该由一次配置变更全体承担。
	s.pool.Sync(specs)

	// 密钥池随配置一起装载。失败不阻断 Reload：没有密钥池时
	// 网关退回单密钥模式，仍然可用——比起整个服务起不来，
	// 这是明显更好的降级。
	if err := s.syncKeyRings(ctx, servers); err != nil {
		s.log.Error("装载密钥池失败，这些 server 将退回单密钥模式", zap.Error(err))
	}

	s.refreshAgg()
	return nil
}

// syncKeyRings 把库里的密钥灌进连接池。
//
// 一次读全部密钥再按 server 分组：按 server 逐个查是典型的 N+1，
// 而 Reload 是管理操作的必经路径，不该随 server 数量线性变慢。
func (s *Service) syncKeyRings(ctx context.Context, servers []store.Server) error {
	all, err := s.store.ListAllCredentials(ctx)
	if err != nil {
		return err
	}

	for _, sv := range servers {
		s.pool.SyncKeyRing(sv.Spec.ID, s.keyRingOptions(sv), toCredentialInputs(all[sv.Spec.ID]))
	}
	return nil
}

// reloadKeyRing 只同步一个 server 的密钥池。
//
// 密钥增删改是逐 server 的操作，走全量 Reload 会顺带重扫所有
// server 与工具缓存，代价与收益不成比例。
func (s *Service) reloadKeyRing(ctx context.Context, serverID string) error {
	sv, err := s.store.GetServer(ctx, serverID)
	if err != nil {
		return err
	}
	list, err := s.store.ListCredentials(ctx, serverID)
	if err != nil {
		return err
	}
	s.pool.SyncKeyRing(serverID, s.keyRingOptions(sv), toCredentialInputs(list))
	return nil
}

func (s *Service) keyRingOptions(sv store.Server) downstream.KeyRingOptions {
	return downstream.KeyRingOptions{
		ServerID:  sv.Spec.ID,
		ServerKey: sv.Spec.Key,
		// 只有远程传输能用密钥池：stdio 的凭据在子进程环境变量里，
		// 改它等于换进程，做不到按调用轮换。
		EstTokensPerCall: sv.EstTokensPerCall,
		Now:              time.Now,
	}
}

func toCredentialInputs(list []store.Credential) []downstream.CredentialInput {
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
			State:   downstream.KeyState(c.State),
			Disabled: downstream.DisabledInfo{
				Kind: c.DisabledKind,
				At:   c.DisabledAt,
				Note: c.DisabledNote,
			},
			RPMLimit: c.RPMLimit,
			TPMLimit: c.TPMLimit,
		})
	}
	return out
}

// 密钥事件在调用日志里的标识。
//
// 它们不是"某次调用失败了"，而是"某把密钥的状态变了"。用独立的一组
// 值，是为了让界面按 status=error 查失败时不会把事件混进来，
// 同时又能专门筛出来回答"这把密钥为什么不转了"。
const (
	logEventKeyCooldown  = "KEY_COOLDOWN"
	logEventKeyDisabled  = "KEY_DISABLED"
	logEventKeyRecovered = "KEY_RECOVERED"
	logEventNoUsableKey  = "NO_USABLE_KEY"
)

// handleKeyEvent 落库密钥状态变化并记一条日志。
func (s *Service) handleKeyEvent(ev KeyEvent) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	event := ""
	switch {
	case ev.Disabled:
		event = logEventKeyDisabled
		if err := s.store.DisableCredential(ctx, ev.KeyID, ev.Verdict, ev.Detail); err != nil {
			s.log.Error("弃用密钥写入失败",
				zap.String("server", ev.ServerKey), zap.String("credential", ev.KeyLabel), zap.Error(err))
		}
		s.log.Warn("密钥已弃用",
			zap.String("server", ev.ServerKey), zap.String("credential", ev.KeyLabel),
			zap.String("verdict", string(ev.Verdict)), zap.String("detail", ev.Detail))

	case ev.CollectiveProtection:
		// 本会被弃用，但留着它总比一把不剩强。把 server 标为降级，
		// 让用户在概览里就看到"这里有事"，而不是等某次调用失败才知道。
		event = logEventNoUsableKey
		if err := s.store.MarkNoUsableKey(ctx, ev.ServerID, ev.Detail); err != nil {
			s.log.Error("标记 server 降级失败", zap.String("server", ev.ServerKey), zap.Error(err))
		}
		s.invalidateServers(ctx)
		s.log.Warn("密钥弃用被集体失败保护拦下",
			zap.String("server", ev.ServerKey), zap.String("credential", ev.KeyLabel), zap.String("detail", ev.Detail))

	case ev.Recovered:
		event = logEventKeyRecovered
		s.log.Info("密钥已从冷却中恢复",
			zap.String("server", ev.ServerKey), zap.String("credential", ev.KeyLabel), zap.String("detail", ev.Detail))

	case ev.Cooling > 0:
		event = logEventKeyCooldown
		s.log.Info("密钥进入冷却",
			zap.String("server", ev.ServerKey), zap.String("credential", ev.KeyLabel),
			zap.Duration("cooldown", ev.Cooling.Round(time.Second)), zap.String("detail", ev.Detail))
	}

	if event == "" {
		return
	}
	s.enqueueLog(LogRecord{
		ServerID:     ev.ServerID,
		ServerKey:    ev.ServerKey,
		Status:       "key",
		Kind:         downstream.ErrorKind(event),
		Err:          ev.Detail,
		CredentialID: ev.KeyID,
	})
}

// RefreshReport 是一次全量刷新的结果。
//
// Total 是**参与探活**的数量，即已启用的 server；停用项单独计入 Disabled。
// 三者满足 Healthy + Failed == Total，Disabled 不在其中。把停用项混进
// Total 会让"我主动关掉的"与"我漏掉的"看起来一样，健康报告就失去意义。
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
//
// 这是故障隔离的关键点：每个 server 在独立 goroutine 里、带独立超时
// 探活，任何一个失败都只写它自己的状态行，不影响其他 server，
// 更不会让整个刷新返回错误。
func (s *Service) RefreshAll(ctx context.Context) *RefreshReport {
	started := time.Now()

	servers, err := s.store.ListServers(ctx)
	if err != nil {
		s.log.Error("刷新前读取 server 列表失败", zap.Error(err))
		return &RefreshReport{Elapsed: time.Since(started)}
	}

	// 先把停用项摘出去：它们不探活、不进结果、也不计入成败。
	// 早先的实现把它们留在 results 里，于是"停用"被报告成"探活失败"，
	// 且 Healthy + Failed 会大于 Total。
	targets := make([]store.Server, 0, len(servers))
	for _, sv := range servers {
		if sv.Enabled {
			targets = append(targets, sv)
		}
	}

	report := &RefreshReport{
		Total:    len(targets),
		Disabled: len(servers) - len(targets),
	}
	// 与 targets 按下标对齐：每个槽位都由对应的 goroutine 写入，
	// 因此 wg.Wait 之后不存在未被填充的零值项。
	results := make([]ServerProbe, len(targets))

	sem := make(chan struct{}, probeConcurrency)
	var wg sync.WaitGroup

	for i, sv := range targets {
		wg.Add(1)
		go func(idx int, sv store.Server) {
			defer wg.Done()

			sem <- struct{}{}
			defer func() { <-sem }()

			res := downstream.NewProber(sv.Spec).Probe(ctx)

			// 探活失败不清空工具缓存：下游抖动不该让已聚合的工具消失。
			// 这条语义由 SaveProbe 保证。
			if err := s.store.SaveProbe(ctx, sv.Spec.ID, res); err != nil {
				s.log.Error("写入探活结果失败",
					zap.String("server", sv.Spec.Key), zap.Error(err))
			}

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
//
// 供管理界面的"测试连通性"按钮使用：测试是只读的，
// 它告诉用户"这个配置能不能用"，要不要让它影响线上工具列表由用户决定。
func (s *Service) ProbeServer(ctx context.Context, serverID string) (*downstream.ProbeResult, error) {
	sv, err := s.store.GetServer(ctx, serverID)
	if err != nil {
		return nil, err
	}

	res := downstream.NewProber(sv.Spec).Probe(ctx)
	if err := s.store.SaveProbe(ctx, serverID, res); err != nil {
		return nil, err
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
//
// SuggestedCategories 只在"新建 + 未指定分类 + 嵌入已启用"时有值：
// 若 top-1 相似度过阈值，分类已自动应用（查 server 可见），
// 其余候选供管理界面让用户选择。
type ApplyResult struct {
	ID                  string              `json:"id"`
	Created             bool                `json:"created"`
	SuggestedCategories []SuggestedCategory `json:"suggestedCategories,omitempty"`
}

// ApplyServer 写入一个新 server 或更新已有配置，并让它立即生效。
//
// 配置变更要不要重建连接，取决于**目标是否换了**，而不是"配置动过没有"。
// 判据是 ConnFingerprint：换密钥、改过滤名单、改显示名都不动连接，
// 只有 url / command / env / 超时 / 协议版本变了才重建。
//
// 这条分界很要紧。拆连接会打断正在跑的调用，而用户点"改个名字"
// 或"轮换密钥"时完全没有这个预期——症状还会以"未知错误"的形式
// 出现在别人的任务里，最难回溯。
func (s *Service) ApplyServer(ctx context.Context, cfg store.Config) (*ApplyResult, error) {
	_, gerr := s.store.GetServerByKey(ctx, cfg.Spec.Key)
	created := errors.Is(gerr, store.ErrNotFound)

	if err := s.store.UpsertServer(ctx, cfg); err != nil {
		return nil, err
	}

	saved, err := s.store.GetServerByKey(ctx, cfg.Spec.Key)
	if err != nil {
		return nil, err
	}

	// 凭据是热更新的：把最新一份交给现有连接，下一个请求就用它。
	// 连接本身不动，在飞调用不受影响——轮换密钥对调用方应当是隐形的。
	// 若这次变更同时动了目标，下面的 Reload 会把这条连接退场，
	// 这次热更新只是白做一次，无害。
	if s.pool.UpdateCredentials(saved.Spec.ID, saved.Spec.Headers) {
		s.log.Info("已热更新下游凭据", zap.String("server", saved.Spec.Key))
	}

	if err := s.Reload(ctx); err != nil {
		return nil, err
	}
	s.invalidateServers(ctx)
	result := &ApplyResult{ID: saved.Spec.ID, Created: created}
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

	// 自动归类（PRD §4.7.3）：新建时未指定分类且嵌入已启用，
	// 用语义相似度匹配已有分类。top-1 过阈值直接应用，
	// top-3 候选始终返回，供前端让用户改。
	if created && saved.CategoryID == "" && s.embed != nil {
		text := saved.Description
		if text == "" {
			text = saved.Spec.Name
		}
		applied, suggestions, err := s.embed.AutoCategorize(ctx, text)
		if err != nil {
			s.log.Warn("自动归类失败，需手动指定",
				zap.String("server", saved.Spec.Key), zap.Error(err))
		} else {
			result.SuggestedCategories = suggestions
			if applied != nil {
				if err := s.store.SetServerCategory(ctx, saved.Spec.ID, applied.ID); err != nil {
					s.log.Warn("自动归类结果保存失败",
						zap.String("server", saved.Spec.Key), zap.Error(err))
				} else {
					s.log.Info("server 已自动归类",
						zap.String("server", saved.Spec.Key),
						zap.String("category", applied.Name))
					s.invalidateServers(ctx)
					s.refreshAgg()
				}
			}
		}
	}
	return result, nil
}

// RefreshTools 探活单个 server，刷新它的工具缓存并重建聚合快照。
//
// 与 ProbeServer 的区别就在最后一步：管理界面点"刷新工具"时，
// 用户期待的是工具列表**真的更新**，而不是只刷新了状态角标。
func (s *Service) RefreshTools(ctx context.Context, serverID string) (*downstream.ProbeResult, error) {
	res, err := s.ProbeServer(ctx, serverID)
	if err != nil {
		return nil, err
	}
	s.refreshAgg()
	return res, nil
}

// refreshAgg 重建聚合快照，同时重建检索索引。
//
// 两者必须同步：检索索引基于 store 里的工具与 server 元数据构建，
// 聚合快照基于探活缓存构建。漏掉任何一边都会让"搜出来的工具
// 在列表里不存在"或反过来。
func (s *Service) refreshAgg() {
	s.agg.Refresh()
	s.rebuildSearchIndex(context.Background())
	// 增量 embed 重建：语料未变时零 API 成本（hash 比对），
	// 让新增 server/工具、描述变更在下一个刷新周期内自动获得向量。
	// Rebuild 自带重入保护，并发调用直接返回现状。
	if s.embed != nil {
		go s.embed.Rebuild(context.Background())
	}
}

// AttachEmbedding 挂上嵌入管理器（可为 nil：未配 API key 或模型时，
// meta 工具照常注册，向量检索走降级路径）。
func (s *Service) AttachEmbedding(em *EmbedManager) {
	s.embed = em
	meta := NewMetaTools(s, s.log).Tools()
	s.agg.SetMetaTools(func() []mcpserver.ServerTool { return meta })
	if em == nil {
		s.log.Info("嵌入检索未启用（未配置 API key 或模型）")
		return
	}
	cfg := em.Config()
	s.log.Info("嵌入检索已启用",
		zap.String("model", cfg.Model),
		zap.Int("dims", cfg.Dimensions),
		zap.Bool("keyConfigured", em.Embedder() != nil),
		zap.Bool("reranker", em.Reranker() != nil))
}

// Categories 返回分类列表（含每个分类的 server 数量）。
func (s *Service) Categories(ctx context.Context) ([]store.Category, error) {
	return s.store.ListCategories(ctx)
}

// CreateCategory 新建一个分类。
func (s *Service) CreateCategory(ctx context.Context, name, description string) (*store.Category, error) {
	cat, err := s.store.CreateCategory(ctx, name, description)
	if err != nil {
		return nil, err
	}
	s.log.Info("分类已创建", zap.String("category", cat.Name))
	return &cat, nil
}

// UpdateCategory 更新分类的名字或描述。
func (s *Service) UpdateCategory(ctx context.Context, id, name, description string) (*store.Category, error) {
	cat, err := s.store.UpdateCategory(ctx, id, name, description)
	if err != nil {
		return nil, err
	}
	s.log.Info("分类已更新", zap.String("category", cat.Name))
	return &cat, nil
}

// DeleteCategory 删除分类。引用它的 server 先被清空分类字段——
// 分类没有外键，不先清引用，列表里就会留着指向不存在分类的 id。
func (s *Service) DeleteCategory(ctx context.Context, id string) error {
	name := ""
	if cat, err := s.store.GetCategory(ctx, id); err == nil {
		name = cat.Name
	}
	if err := s.store.DeleteCategory(ctx, id); err != nil {
		return err
	}
	s.log.Info("分类已删除", zap.String("category", name), zap.String("id", id))
	s.invalidateServers(ctx)
	s.refreshAgg()
	return nil
}

// Embed 返回嵌入管理器（未启用时为 nil）。
func (s *Service) Embed() *EmbedManager { return s.embed }

// Servers 返回全部 server 的当前状态（含最近一次探活结果）。
//
// 直接读库而不是读聚合快照：快照只包含已启用的 server，
// 而管理界面必须能看到被禁用的那些——否则用户停用一个 server 后
// 就再也找不到它了。
//
// 结果走缓存：概览页会周期性拉这个列表，而每一行都带着完整配置。
// 所有写路径（保存、启停、删除、探活、密钥集体失败）都会同步删掉
// 缓存，所以"改完立刻读回来"拿到的仍然是新值。
func (s *Service) Servers(ctx context.Context) ([]store.Server, error) {
	return s.servers.GetOrLoad(ctx, serversKey, serversTTL, s.store.ListServers)
}

// RecentStats 返回最近 window 时间窗内的调用统计。
func (s *Service) RecentStats(ctx context.Context, window time.Duration) (store.CallStats, error) {
	return s.stats.GetOrLoad(ctx, window.String(), statsTTL,
		func(ctx context.Context) (store.CallStats, error) {
			return s.store.Stats(ctx, time.Now().Add(-window))
		})
}

// invalidateServers 让 server 列表缓存立即失效。
//
// 必须由写路径同步调用，不能只靠 TTL 自然过期：管理界面"改完立刻
// 读回来"是最基本的预期，而缓存晚几秒过期在用户看来就是"这次没存上"。
func (s *Service) invalidateServers(ctx context.Context) {
	// 脱离取消：写操作已经提交了，这时候请求上下文可能已经被取消，
	// 而缓存删不掉会留下一个过期视图。
	ctx = context.WithoutCancel(ctx)
	if err := s.servers.InvalidateAll(ctx); err != nil {
		s.log.Warn("清理 server 列表缓存失败", zap.Error(err))
	}
}

// RemoveServer 删除一个 server 并让聚合列表立即反映这一变化。
//
// 删记录是立刻的（工具马上从聚合列表消失），连接则优雅退场——
// 已经跑起来的调用不会因为运维删了一行配置就被打断。
func (s *Service) RemoveServer(ctx context.Context, serverID string) error {
	key := s.serverKey(ctx, serverID)
	if err := s.store.DeleteServer(ctx, serverID); err != nil {
		return err
	}
	s.pool.Retire(serverID)
	if err := s.Reload(ctx); err != nil {
		return err
	}
	s.invalidateServers(ctx)
	s.log.Info("server 已删除", zap.String("server", key))
	return nil
}

// SetEnabled 启停一个 server。
//
// 停用分两步落地，顺序不能反：先把工具从聚合列表摘掉（AI 从此不会再
// 派新调用过来），再让连接优雅退场（已经在跑的那些跑完才关）。
// 这就是用户点"停用"时真正想要的语义——别再被调用了，
// 而不是把正在做的事拦腰截断。
func (s *Service) SetEnabled(ctx context.Context, serverID string, enabled bool) error {
	key := s.serverKey(ctx, serverID)
	if err := s.store.SetEnabled(ctx, serverID, enabled); err != nil {
		return err
	}
	if !enabled {
		s.pool.Retire(serverID)
	}
	if err := s.Reload(ctx); err != nil {
		return err
	}
	s.invalidateServers(ctx)
	if enabled {
		s.log.Info("server 已启用", zap.String("server", key))
	} else {
		s.log.Info("server 已停用", zap.String("server", key))
	}
	return nil
}

// DrainingInfo 返回某个 server 的停用收尾进度。
//
// 停用不再是一个瞬时动作：如果当时有调用在跑，连接要等它们结束。
// 管理界面据此显示"停用中 · 还有 N 个调用在收尾"。
func (s *Service) DrainingInfo(serverID string) (DrainingInfo, bool) {
	return s.pool.DrainingOf(serverID)
}

// Ready 报告网关是否已有可用的聚合结果。
//
// 判据是"有工具可提供"，而不是"所有下游都健康"——后者会让网关
// 在任何一个下游抖动时被判定为未就绪，恰恰违反了故障隔离的初衷。
func (s *Service) Ready() (bool, []string) {
	snap := s.reg.Current()
	var unhealthy []string
	for _, sv := range snap.Servers() {
		if !sv.Available {
			unhealthy = append(unhealthy, sv.ServerKey)
		}
	}
	return snap.ToolCount() > 0, unhealthy
}

// Close 停止日志 worker 并关闭全部下游连接。
func (s *Service) Close() error {
	close(s.closeCh)
	s.closeWG.Wait()

	err := s.pool.Close()
	// 缓存的关闭错误不该盖掉连接池的：后者才是会漏资源的那一个。
	if cerr := s.cache.Close(); cerr != nil && err == nil {
		err = cerr
	}
	return err
}

// --- 密钥池管理 ---

// ErrKeyPoolUnsupported 表示该接入方式不支持密钥池。
var ErrKeyPoolUnsupported = errors.New("该接入方式不支持密钥池")

// CredentialSpec 是新增或编辑一把密钥的输入。
//
// headres 是完整的一组注入头，而不是"某个头字段"：供应商的凭据形态
// 各异（Bearer / X-API-Key / 多字段签名），只有整组才是一次可独立
// 轮换的最小单位。
type CredentialSpec struct {
	Label    string
	Headers  map[string]string
	RPMLimit int
	TPMLimit int
}

// Credentials 返回某个 server 的密钥配置与运行时状态。
func (s *Service) Credentials(ctx context.Context, serverID string) ([]store.Credential, []downstream.KeyStatus, error) {
	if _, err := s.store.GetServer(ctx, serverID); err != nil {
		return nil, nil, err
	}
	list, err := s.store.ListCredentials(ctx, serverID)
	if err != nil {
		return nil, nil, err
	}
	return list, s.pool.KeyStatusesOf(serverID), nil
}

// AllKeyStatuses 返回全部 server 的密钥运行状态，键为 serverId。
func (s *Service) AllKeyStatuses() map[string][]downstream.KeyStatus {
	return s.pool.KeyStatuses()
}

// AddCredential 新增一把密钥并让它立即参与轮换。
func (s *Service) AddCredential(ctx context.Context, serverID string, in CredentialSpec) (store.Credential, error) {
	sv, err := s.requireKeyPoolSupport(ctx, serverID)
	if err != nil {
		return store.Credential{}, err
	}
	c, err := s.store.InsertCredential(ctx, store.Credential{
		ServerID: serverID,
		Label:    strings.TrimSpace(in.Label),
		Headers:  in.Headers,
		Enabled:  true,
		RPMLimit: max(in.RPMLimit, 0),
		TPMLimit: max(in.TPMLimit, 0),
	})
	if err != nil {
		return store.Credential{}, err
	}
	s.log.Info("密钥已添加",
		zap.String("server", sv.Spec.Key),
		zap.String("credential", c.LabelOrFallback()),
		zap.String("id", c.ID))
	return c, s.reloadKeyRing(ctx, serverID)
}

// ImportCredentials 批量追加密钥。
func (s *Service) ImportCredentials(ctx context.Context, serverID string, in []CredentialSpec) ([]store.Credential, error) {
	sv, err := s.requireKeyPoolSupport(ctx, serverID)
	if err != nil {
		return nil, err
	}
	if len(in) == 0 {
		return nil, nil
	}

	creds := make([]store.Credential, 0, len(in))
	for _, item := range in {
		creds = append(creds, store.Credential{
			Label:    strings.TrimSpace(item.Label),
			Headers:  item.Headers,
			Enabled:  true,
			RPMLimit: max(item.RPMLimit, 0),
			TPMLimit: max(item.TPMLimit, 0),
		})
	}

	out, err := s.store.InsertCredentials(ctx, serverID, creds)
	if err != nil {
		return nil, err
	}
	s.log.Info("密钥已批量导入",
		zap.String("server", sv.Spec.Key), zap.Int("count", len(out)))
	return out, s.reloadKeyRing(ctx, serverID)
}

// UpdateCredential 编辑一把密钥。
//
// 刻意不允许改状态：状态是判决结果，改配置不该顺手把它复位，
// 否则"这把密钥为什么被禁"的线索会凭空消失。要重新启用请走
// SetCredentialEnabled。
func (s *Service) UpdateCredential(ctx context.Context, serverID, credID string, in CredentialSpec) (store.Credential, error) {
	sv, err := s.requireKeyPoolSupport(ctx, serverID)
	if err != nil {
		return store.Credential{}, err
	}
	existing, err := s.credentialOf(ctx, serverID, credID)
	if err != nil {
		return store.Credential{}, err
	}

	existing.Label = strings.TrimSpace(in.Label)
	if in.Headers != nil {
		existing.Headers = in.Headers
	}
	existing.RPMLimit = max(in.RPMLimit, 0)
	existing.TPMLimit = max(in.TPMLimit, 0)

	if err := s.store.UpdateCredential(ctx, existing); err != nil {
		return store.Credential{}, err
	}
	if err := s.reloadKeyRing(ctx, serverID); err != nil {
		return existing, err
	}
	saved, err := s.credentialOf(ctx, serverID, credID)
	if err != nil {
		return existing, err
	}
	s.log.Info("密钥已更新",
		zap.String("server", sv.Spec.Key),
		zap.String("credential", saved.LabelOrFallback()),
		zap.String("id", credID))
	return saved, nil
}

// DeleteCredential 删除一把密钥。
func (s *Service) DeleteCredential(ctx context.Context, serverID, credID string) error {
	cred, err := s.credentialOf(ctx, serverID, credID)
	if err != nil {
		return err
	}
	if err := s.store.DeleteCredential(ctx, credID); err != nil {
		return err
	}
	s.log.Info("密钥已删除",
		zap.String("server", s.serverKey(ctx, serverID)),
		zap.String("credential", cred.LabelOrFallback()),
		zap.String("id", credID))
	return s.reloadKeyRing(ctx, serverID)
}

// SetCredentialEnabled 人工启停一把密钥。
//
// 启用会顺带清掉冷却：用户点"启用"的意图就是"我现在要用它"，
// 残留的冷却会让这个动作静默失效——点完之后还是不用，
// 而界面上那两个按钮长得一模一样。
func (s *Service) SetCredentialEnabled(ctx context.Context, serverID, credID string, enabled bool) error {
	cred, err := s.credentialOf(ctx, serverID, credID)
	if err != nil {
		return err
	}
	if err := s.store.SetCredentialEnabled(ctx, credID, enabled); err != nil {
		return err
	}
	if enabled {
		if ring := s.pool.KeyRing(serverID); ring != nil {
			ring.ClearCooling(credID)
		}
	}
	action := "server 密钥已停用"
	if enabled {
		action = "server 密钥已启用"
	}
	s.log.Info(action,
		zap.String("server", s.serverKey(ctx, serverID)),
		zap.String("credential", cred.LabelOrFallback()),
		zap.String("id", credID))
	return s.reloadKeyRing(ctx, serverID)
}

// ResetCredentialQuota 清零一把密钥的本地配额计数。
//
// 只清计数，不动冷却：本地计数是网关的保守估计，用户比网关更清楚
// 真实配额；而冷却是下游给的客观信号，清掉它等于骗自己。
func (s *Service) ResetCredentialQuota(ctx context.Context, serverID, credID string) error {
	cred, err := s.credentialOf(ctx, serverID, credID)
	if err != nil {
		return err
	}
	ring := s.pool.KeyRing(serverID)
	if ring == nil || !ring.ResetQuota(credID) {
		return store.ErrNotFound
	}
	s.log.Info("密钥配额计数已清零",
		zap.String("server", s.serverKey(ctx, serverID)),
		zap.String("credential", cred.LabelOrFallback()),
		zap.String("id", credID))
	return nil
}

// TestCredential 用单把密钥做一次探活。
//
// 只读：不改密钥状态、不动工具缓存。与 FR-2.4 的语义一致——
// 测试回答"这个配置现在能用吗"，要不要让它影响线上由用户决定。
//
// 这里刻意不按判决弃用密钥：连续次数（比如 403 的两次阈值）是状态机
// 的一部分，绕过它单独禁用会让手工测试与在线调用的结论互相矛盾。
func (s *Service) TestCredential(ctx context.Context, serverID, credID string) (*downstream.ProbeResult, downstream.KeyVerdict, error) {
	sv, err := s.store.GetServer(ctx, serverID)
	if err != nil {
		return nil, "", err
	}
	cred, err := s.credentialOf(ctx, serverID, credID)
	if err != nil {
		return nil, "", err
	}

	spec := sv.Spec
	spec.Headers = cred.Headers
	res := downstream.NewProber(spec).Probe(ctx)

	verdict := downstream.VerdictOK
	if !res.OK {
		verdict = downstream.VerdictOf(res.Kind, res.Observation())
	}
	return res, verdict, nil
}

// KeySummary 是密钥池的整体状况，供概览展示。
//
// 三种"用不上"的原因分开计数：把它们合成一个数字会丢掉用户唯一
// 需要的那个线索——冷却会自己好，弃用需要换密钥，人工停用需要人改
// 主意，三种下一步动作完全不同。
type KeySummary struct {
	Total int `json:"total"`
	// Servers 是配了密钥池的 server 数（未配的走单密钥模式，不在统计内）。
	Servers int `json:"servers"`

	Usable   int `json:"usable"`
	Cooling  int `json:"cooling"`
	Disabled int `json:"disabled"`

	// Unusable 是 Cooling + Disabled 的合计，直接给前端用：
	// 让它在两处分别相加，早晚会有一处漏掉。
	Unusable int `json:"unusable"`
}

// KeySummary 汇总全部 server 的密钥状态。
func (s *Service) KeySummary(ctx context.Context) (KeySummary, error) {
	all, err := s.store.ListAllCredentials(ctx)
	if err != nil {
		return KeySummary{}, err
	}
	live := s.pool.KeyStatuses()

	var out KeySummary
	for serverID, creds := range all {
		if len(creds) == 0 {
			continue
		}
		out.Servers++

		byID := make(map[string]downstream.KeyStatus, len(live[serverID]))
		for _, st := range live[serverID] {
			byID[st.ID] = st
		}

		for _, c := range creds {
			out.Total++
			st, ok := byID[c.ID]
			if !ok {
				// 池子还没装载（比如 server 停用中）：退到持久状态。
				// 宁可显示旧的结论，也不要显示一个看起来正常的空壳。
				if c.Usable() {
					out.Usable++
				} else {
					out.Disabled++
				}
				continue
			}
			switch st.State {
			case downstream.KeyCooling:
				out.Cooling++
			case downstream.KeyDisabled:
				out.Disabled++
			default:
				out.Usable++
			}
		}
	}
	out.Unusable = out.Cooling + out.Disabled
	return out, nil
}

// ActiveKeyCounts 返回每个 server 当前可参与轮换的密钥数。
func (s *Service) ActiveKeyCounts() map[string]int {
	p := s.pool.KeyStatuses()
	out := make(map[string]int, len(p))
	for serverID, list := range p {
		n := 0
		for _, k := range list {
			if k.State == downstream.KeyActive {
				n++
			}
		}
		out[serverID] = n
	}
	return out
}

// CredentialHeaders 返回一把密钥的完整注入头。
//
// 只给内部用（编辑时把回显的 *** 换回真值）：明文绝不出现在任何
// 对外响应里，这个方法的调用点也就必须留在管理 API 的入参处理那一步。
func (s *Service) CredentialHeaders(ctx context.Context, serverID, credID string) (map[string]string, error) {
	c, err := s.credentialOf(ctx, serverID, credID)
	if err != nil {
		return nil, err
	}
	return c.Headers, nil
}

func (s *Service) requireKeyPoolSupport(ctx context.Context, serverID string) (store.Server, error) {
	sv, err := s.store.GetServer(ctx, serverID)
	if err != nil {
		return store.Server{}, err
	}
	if !sv.Spec.IsRemote() {
		return store.Server{}, fmt.Errorf(
			"%w：stdio 的凭据在子进程的环境变量里，改它需要重启进程，无法按调用轮换", ErrKeyPoolUnsupported)
	}
	return sv, nil
}

// credentialOf 读取一把密钥并确认它确实属于该 server。
//
// 校验归属不能省：密钥 id 是全局唯一的，漏掉这一步就意味着
// 拿着 A 的密钥 id 能改到 B 的配置上。
func (s *Service) credentialOf(ctx context.Context, serverID, credID string) (store.Credential, error) {
	c, err := s.store.GetCredential(ctx, credID)
	if err != nil {
		return store.Credential{}, err
	}
	if c.ServerID != serverID {
		return store.Credential{}, store.ErrNotFound
	}
	return c, nil
}

// --- 内部 ---

// sources 从存储读出聚合输入。
//
// 每次都读库而不是维护内存副本：Refresh 是低频操作，而 SQLite
// 本地读是微秒级。多一份内存缓存就多一处可能与库不一致的状态。
func (s *Service) sources() []ToolSource {
	ctx := context.Background()

	servers, err := s.store.ListServers(ctx)
	if err != nil {
		s.log.Error("聚合时读取 server 列表失败", zap.Error(err))
		return nil
	}
	tools, err := s.store.ListTools(ctx)
	if err != nil {
		s.log.Error("聚合时读取工具缓存失败", zap.Error(err))
		return nil
	}

	byServer := make(map[string][]downstream.ToolInfo, len(servers))
	for _, t := range tools {
		byServer[t.ServerID] = append(byServer[t.ServerID], downstream.ToolInfo{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: json.RawMessage(t.InputSchema),
		})
	}

	out := make([]ToolSource, 0, len(servers))
	for _, sv := range servers {
		out = append(out, ToolSource{
			ServerID:  sv.Spec.ID,
			ServerKey: sv.Spec.Key,
			Name:      sv.Spec.Name,
			Enabled:   sv.Enabled,
			// healthy 与 degraded 都算可用：degraded 的语义是
			// "能连但不完美"，不该被当成不能用。
			Available: sv.Status == "healthy" || sv.Status == "degraded",
			Tools:     applyToolFilter(byServer[sv.Spec.ID], sv.ToolFilterMode, sv.ToolFilterList),
		})
	}
	return out
}

func (s *Service) specOf(serverID string) (downstream.Spec, bool) {
	s.specs.RLock()
	defer s.specs.RUnlock()
	spec, ok := s.specMap[serverID]
	return spec, ok
}

// serverKey 返回日志用的展示 key；查不到时退回 id，
// 删除路径上 server 已不存在也仍要有一条带名字的日志。
func (s *Service) serverKey(ctx context.Context, serverID string) string {
	if sv, err := s.store.GetServer(ctx, serverID); err == nil {
		return sv.Spec.Key
	}
	return serverID
}

// enqueueLog 把调用日志投入异步队列。
//
// 队列满时直接丢弃而不是阻塞：日志是观测手段，
// 不该成为调用路径上的一个可用性依赖。
func (s *Service) enqueueLog(rec LogRecord) {
	entry := store.LogEntry{
		ServerID:     rec.ServerID,
		ServerKey:    rec.ServerKey,
		ToolName:     rec.ToolName,
		ExposedName:  rec.Exposed,
		DurationMS:   rec.Duration.Milliseconds(),
		Status:       rec.Status,
		ErrorKind:    rec.Kind,
		Error:        rec.Err,
		CredentialID: rec.CredentialID,
		APIKeyID:     rec.APIKeyID,
		Args:         rec.Args,
		Result:       rec.Result,
	}

	select {
	case s.logCh <- entry:
	case <-s.closeCh:
	default:
		s.log.Debug("调用日志队列已满，丢弃一条记录", zap.String("tool", rec.Exposed))
	}
}

// logWorker 批量落库，摊薄 SQLite 的写事务开销。
func (s *Service) logWorker() {
	defer s.closeWG.Done()

	const (
		batchSize    = 32
		flushTimeout = 500 * time.Millisecond
	)

	batch := make([]store.LogEntry, 0, batchSize)
	ticker := time.NewTicker(flushTimeout)
	defer ticker.Stop()

	flush := func() {
		if len(batch) == 0 {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for _, e := range batch {
			if err := s.store.AppendLog(ctx, e); err != nil {
				s.log.Error("写入调用日志失败", zap.Error(err))
				break
			}
		}
		batch = batch[:0]
	}

	for {
		select {
		case <-s.closeCh:
			// 退出前把队列里剩下的日志冲干净。
		drain:
			for {
				select {
				case e := <-s.logCh:
					batch = append(batch, e)
				default:
					break drain
				}
			}
			flush()
			return

		case e := <-s.logCh:
			batch = append(batch, e)
			if len(batch) >= batchSize {
				flush()
			}

		case <-ticker.C:
			flush()
		}
	}
}

// ErrServerNotFound 让上层区分"server 不存在"与真正的存储错误。
var ErrServerNotFound = store.ErrNotFound

// IsNotFound 报告错误是否为记录不存在。
func IsNotFound(err error) bool { return errors.Is(err, store.ErrNotFound) }

// applyToolFilter 按 server 的过滤规则筛选它的工具。
//
// 过滤发生在聚合这一步，而不是写工具缓存的时候：用户改一次名单
// 就该立刻生效，不该被迫重新探活一遍下游。缓存保留原始清单，
// 名单只是"这次暴露哪些"的视图。
func applyToolFilter(
	tools []downstream.ToolInfo,
	mode store.ToolFilterMode,
	list []string,
) []downstream.ToolInfo {
	if len(tools) == 0 {
		return tools
	}

	switch mode.Normalize() {
	case store.FilterAllowAll:
		return tools
	case store.FilterAllow:
		allow := nameSet(list)
		// 名单为空时不过滤。这条规则与 store 层的校验重复，是有意的：
		// 校验挡住的是新写入，这里兜的是历史脏数据，
		// 而"工具全部消失"是绝不能发生的那一种失败。
		if len(allow) == 0 {
			return tools
		}
		out := make([]downstream.ToolInfo, 0, len(tools))
		for _, t := range tools {
			if _, ok := allow[t.Name]; ok {
				out = append(out, t)
			}
		}
		return out
	case store.FilterBlock:
		block := nameSet(list)
		if len(block) == 0 {
			return tools
		}
		out := make([]downstream.ToolInfo, 0, len(tools))
		for _, t := range tools {
			if _, ok := block[t.Name]; !ok {
				out = append(out, t)
			}
		}
		return out
	}
	return tools
}

func nameSet(names []string) map[string]struct{} {
	if len(names) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(names))
	for _, n := range names {
		if trimmed := strings.TrimSpace(n); trimmed != "" {
			set[trimmed] = struct{}{}
		}
	}
	return set
}
