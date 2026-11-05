// Package hub 是聚合网关的门面：把 registry（发给谁）、pool（怎么发）、
// aggregate（对外长什么样）、retrieval（怎么被搜到）装配成 Service，
// 并承载管理面。子包经 Deps 注入与回调反向依赖，不认识 Service，故无环。
package hub

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hydra/mcp-hub/internal/cache"
	"github.com/hydra/mcp-hub/internal/downstream"
	"github.com/hydra/mcp-hub/internal/hub/aggregate"
	"github.com/hydra/mcp-hub/internal/hub/pool"
	"github.com/hydra/mcp-hub/internal/hub/registry"
	"github.com/hydra/mcp-hub/internal/hub/retrieval"
	"github.com/hydra/mcp-hub/internal/store"
	"github.com/hydra/mcp-hub/internal/store/categories"
	"github.com/hydra/mcp-hub/internal/store/logs"
	"github.com/hydra/mcp-hub/internal/store/servers"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"go.uber.org/zap"
)

// probeConcurrency 是并发探活的上限，避免对下游造成突发流量。
const probeConcurrency = 8

// aggRefreshGap 是错峰探活路径上聚合快照重建的最小间隔。
// 被节流掉的重建由下一个到期的探活补上，变更可见延迟最多一个节拍。
const aggRefreshGap = time.Second

// logQueueSize 是调用日志的异步队列长度。
const logQueueSize = 512

// keyEventQueueSize 是密钥事件的异步队列长度：状态变化发生在调用路径上，
// 写库必须挪出去，容量按"几十把密钥同时异常"留足余量。
const keyEventQueueSize = 256

// serversTTL 是 server 列表的缓存时长。写路径会同步删缓存，
// 这个 TTL 只用来兜住没有明确写请求的状态变化（周期探活改的是 status）。
const serversTTL = 2 * time.Second

// statsTTL 是调用统计的缓存时长，比列表长：它扫整张日志表，
// 而日志本身异步批量落库，这份数字本来就带滞后。
const statsTTL = 5 * time.Second

// serversKey 是 server 列表缓存里唯一那个 key。
const serversKey = "all"

// Service 把配置、连接、聚合编排成整体：所有影响"工具长什么样"的操作
// 都从这里过，保证 Registry 与 Pool 不会各自为政。
type Service struct {
	store *store.Store
	pool  *pool.Pool
	reg   *registry.Registry
	agg   *aggregate.Aggregator
	retr  *retrieval.Engine
	log   *zap.Logger

	specs   sync.RWMutex
	specMap map[string]downstream.Spec

	// 缓存只挂在"管理界面反复读、写路径能立刻删掉"的几处。
	// 调用路径一次库都不查：聚合结果本来就在 Registry 里。
	cache   cache.Store
	servers *cache.Typed[[]servers.Server]
	stats   *cache.Typed[logs.CallStats]

	logCh   chan logs.LogEntry
	closeCh chan struct{}
	closeWG sync.WaitGroup

	// 密钥事件队列：入队在调用路径（非阻塞），落库在 keyEventWorker。
	keyEventCh chan pool.KeyEvent

	// probeSem 是错峰探活的并发上限，与 RefreshAll 的局部信号量相互独立：
	// 手动全量刷新允许和周期探活短暂并存。
	probeSem chan struct{}

	// credTestSem 是凭据自动试测的名额信号量，单条新增与批量导入共用，
	// 保证对下游的试测流量整体有界。
	credTestSem chan struct{}

	// credTestTimeout 是单把密钥试测的超时。零值取 credentialTestTimeout。
	credTestTimeout time.Duration

	// probeFn 是凭据试测的探活接缝，留空用真实的 Prober，只有测试会替换它。
	probeFn func(ctx context.Context, spec downstream.Spec) *downstream.ProbeResult

	// 连败拦截闸：连续探活失败达到阈值后拒绝该上游的调用，
	// 探活不受影响。见 service_block.go。
	gate *blockGate

	// 聚合快照上次重建时刻（UnixNano），refreshAggThrottled 用它节流。
	lastAggRefresh atomic.Int64

	// reloadFn 覆盖 Reload 的实现（见 Options.ReloadFn），生产恒为 nil。
	reloadFn func(context.Context) error

	// reloadKeyRingFn 覆盖 reloadKeyRing 的实现（见 Options.ReloadKeyRingFn），
	// 生产恒为 nil。
	reloadKeyRingFn func(context.Context, string) error

	// last_used_at 的落库节流（见 touchAPIKey）。
	touchMu   sync.Mutex
	touchLast map[string]time.Time

	// credWriteMu 是凭据写路径（新增/编辑/导入/删除）的全局互斥锁：
	// 同值检测与落库必须在同一临界区完成，否则并发新增会留下两份同值凭据；
	// 检测跨 server，故不能按 server 分锁；探活/试测走网络，不在此锁内。
	credWriteMu sync.Mutex

	// credCheckedHook 是测试接缝：在写临界区内、同值检测通过后、落库前被调用，
	// 生产恒为 nil。
	credCheckedHook func()
}

// Options 是 NewServiceWithOptions 的可选参数。
type Options struct {
	// Cache 是缓存后端，必填（生产用 Redis，测试用 cachetest）。
	Cache cache.Store
	// ListMode 控制 AI 客户端 tools/list 看到什么（meta | all）。
	// 库默认 all；CLI 默认 meta（见 cmd/mcp-hub）。
	ListMode string
	// BlockAfterFails 是连败拦截阈值：同一 server 连续探活失败达到该次数
	// 后，拒绝它的工具调用（探活照常）。0 或负数关闭拦截。
	BlockAfterFails int
	// ReloadFn 替换配置重载（Reload）的实现，留空用 Reload 本体。
	// 写已落库后刷新失败只应记为运行时状态问题，不能把已提交的写判成失败；
	// 只有测试会设置它。
	ReloadFn func(context.Context) error
	// ReloadKeyRingFn 替换单个 server 的密钥池同步（reloadKeyRing）的实现，
	// 留空用本体。凭据增删改只同步自己那个 server 的密钥池，失败同样只是
	// 运行时状态没跟上，不该把已提交的删除或启停报成失败；只有测试会设置它。
	ReloadKeyRingFn func(context.Context, string) error
}

// NewServiceWithCache 用指定的缓存后端构造服务并完成首次聚合装载。
// 首次装载只读已落库的缓存、不触发探活，服务可立刻提供上一次已知的工具列表。
func NewServiceWithCache(st *store.Store, logger *zap.Logger, c cache.Store) *Service {
	return NewServiceWithOptions(st, logger, Options{Cache: c})
}

// NewServiceWithOptions 是构造服务的全量入口。
func NewServiceWithOptions(st *store.Store, logger *zap.Logger, opts Options) *Service {
	if logger == nil {
		logger = zap.NewNop()
	}
	if opts.Cache == nil {
		panic("hub: Options.Cache 必填")
	}
	if opts.ListMode == "" {
		opts.ListMode = aggregate.ListModeAll
	}

	s := &Service{
		store:           st,
		pool:            pool.NewPool(logger),
		reg:             registry.NewRegistry(),
		retr:            retrieval.NewEngine(st, logger),
		log:             logger,
		specMap:         make(map[string]downstream.Spec),
		cache:           opts.Cache,
		servers:         cache.NewTyped[[]servers.Server](opts.Cache, "hub:servers"),
		stats:           cache.NewTyped[logs.CallStats](opts.Cache, "hub:stats"),
		logCh:           make(chan logs.LogEntry, logQueueSize),
		closeCh:         make(chan struct{}),
		keyEventCh:      make(chan pool.KeyEvent, keyEventQueueSize),
		probeSem:        make(chan struct{}, probeConcurrency),
		credTestSem:     make(chan struct{}, credentialTestConcurrency),
		gate:            newBlockGate(opts.BlockAfterFails),
		reloadFn:        opts.ReloadFn,
		reloadKeyRingFn: opts.ReloadKeyRingFn,
	}

	s.agg = aggregate.NewAggregator(aggregate.Deps{
		Sources:       s.sources,
		SpecOf:        s.specOf,
		Log:           s.enqueueLog,
		Blocked:       s.gate.blocked,
		OnCallSuccess: s.gate.reset,
	}, s.reg, s.pool, opts.ListMode)

	// 密钥状态变化由连接池回调到这里落库与记日志：
	// 连接池不该知道存储的存在。
	s.pool.OnKeyEvent = s.handleKeyEvent

	if err := s.Reload(context.Background()); err != nil {
		s.log.Error("首次装载聚合快照失败", zap.Error(err))
	}

	s.closeWG.Add(3)
	go s.logWorker()
	go s.keyEventWorker()
	s.startKeyReviver()

	return s
}

// Aggregator 返回聚合器，供 HTTP 层挂载 MCP 端点。
func (s *Service) Aggregator() *aggregate.Aggregator { return s.agg }

// Registry 返回聚合路由表。
func (s *Service) Registry() *registry.Registry { return s.reg }

// Pool 返回连接池。
func (s *Service) Pool() *pool.Pool { return s.pool }

// Store 返回持久层。
func (s *Service) Store() *store.Store { return s.store }

// refreshAgg 重建聚合快照，同时重建检索索引。
// 两者必须同步，否则会出现搜得到却在列表里不存在的工具，或反过来。
func (s *Service) refreshAgg() {
	s.lastAggRefresh.Store(time.Now().UnixNano())
	s.agg.Refresh()
	s.retr.RebuildIndex(context.Background())
	// 增量 embed 重建：语料未变时零 API 成本（hash 比对）。
	// Rebuild 自带重入保护，已有重建在跑时直接返回 skipped，不排队也不等待。
	if em := s.retr.Embedder(); em != nil {
		go em.Rebuild(context.Background())
	}
}

// refreshAggThrottled 是错峰探活专用的节流版 refreshAgg。
// 相邻两次重建不足 aggRefreshGap 时跳过，跳过的变更由下一个到期的探活补上。
func (s *Service) refreshAggThrottled() {
	now := time.Now().UnixNano()
	last := s.lastAggRefresh.Load()
	if now-last < int64(aggRefreshGap) {
		return
	}
	if !s.lastAggRefresh.CompareAndSwap(last, now) {
		return
	}
	s.refreshAgg()
}

// AttachEmbedding 挂上嵌入管理器，可为 nil（未配 API key 或模型时
// meta 工具照常注册，向量检索走降级路径）。
func (s *Service) AttachEmbedding(em *retrieval.EmbedManager) {
	s.retr.AttachEmbedder(em)
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

// SearchTools 按意图检索工具，实现见 internal/hub/retrieval。
// 转发一层是为了让调用方（meta-tool 与管理接口）不必自己持有检索引擎。
func (s *Service) SearchTools(ctx context.Context, q retrieval.SearchQuery) ([]retrieval.ToolHit, error) {
	return s.retr.Search(ctx, q)
}

// Embed 返回嵌入管理器（未启用时为 nil）。
func (s *Service) Embed() *retrieval.EmbedManager { return s.retr.Embedder() }

// Ready 报告网关是否已有可用的聚合结果。
// 判据是"有工具可提供"而非"所有下游都健康"，否则单个下游抖动就会让网关未就绪。
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

// sources 从存储读出聚合输入。每次读库而不维护内存副本：
// Refresh 低频、本地读微秒级，多一份缓存就多一处可能与库不一致的状态。
// 任一读取失败返回 ok=false，调用方保留上一份快照，避免瞬时错误演变成"工具全部消失"。
func (s *Service) sources() ([]registry.ToolSource, bool) {
	ctx := context.Background()

	srvs, err := s.store.Servers.List(ctx)
	if err != nil {
		s.log.Error("聚合时读取 server 列表失败", zap.Error(err))
		return nil, false
	}
	tools, err := s.store.Servers.ListTools(ctx)
	if err != nil {
		s.log.Error("聚合时读取工具缓存失败", zap.Error(err))
		return nil, false
	}
	cats, err := s.store.Categories.List(ctx)
	if err != nil {
		s.log.Error("聚合时读取分类失败", zap.Error(err))
		return nil, false
	}
	// 分类只取中文展示名：它不进对外工具名（serverKey__工具名），
	// 只用于展示、过滤与词法检索，不需要 slug。
	catByID := make(map[string]categories.Category, len(cats))
	for _, c := range cats {
		catByID[c.ID] = c
	}

	// 缓存里的工具分两堆：启用着的进聚合，人工关掉的只留给管理界面，
	// 否则关掉就再也找不回来。
	enabledByServer := make(map[string][]downstream.ToolInfo, len(srvs))
	disabledByServer := make(map[string][]registry.HiddenTool, len(srvs))
	for _, t := range tools {
		info := downstream.ToolInfo{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: json.RawMessage(t.InputSchema),
		}
		if !t.Enabled {
			disabledByServer[t.ServerID] = append(disabledByServer[t.ServerID],
				registry.HiddenTool{Info: info})
			continue
		}
		enabledByServer[t.ServerID] = append(enabledByServer[t.ServerID], info)
	}

	out := make([]registry.ToolSource, 0, len(srvs))
	for _, sv := range srvs {
		cat := catByID[sv.CategoryID]
		out = append(out, registry.ToolSource{
			ServerID:  sv.Spec.ID,
			ServerKey: sv.Spec.Key,
			Name:      sv.Spec.Name,
			Category:  cat.Name,
			Enabled:   sv.Enabled,
			// healthy 与 degraded 都算可用：degraded 的语义是
			// "能连但不完美"，不该被当成不能用。
			Available: sv.Status == "healthy" || sv.Status == "degraded",
			Tools:     enabledByServer[sv.Spec.ID],
			Hidden:    disabledByServer[sv.Spec.ID],
		})
	}
	return out, true
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
	if sv, err := s.store.Servers.Get(ctx, serverID); err == nil {
		return sv.Spec.Key
	}
	return serverID
}
