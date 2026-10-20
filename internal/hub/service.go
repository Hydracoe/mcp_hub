// Package hub 是聚合网关的门面：把连接、路由、对外端点与检索四块能力
// 装配成一个 Service，并承载管理面（配置装载、密钥池、分类、调用日志、
// meta-tool 与接入鉴权）。
//
// 四块能力在各自的子包里：registry 决定"发给谁"、pool 决定"怎么发"、
// aggregate 是"对外长什么样"、retrieval 回答"怎么被搜到"，callctx 是它们
// 共享的一次调用的上下文。子包都不认识 Service——依赖靠 Deps 注入与
// OnKeyEvent 回调反向传递，因此没有环。
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

// probeConcurrency 是并发探活的上限：不设限会让 50 个 server 同时建连，
// 对下游是突发流量，对本机是瞬间拉满的socket。
const probeConcurrency = 8

// aggRefreshGap 是错峰探活路径上聚合快照重建的最小间隔。错峰模式下每秒
// 至多一两个 server 出结果，不节流会让几千工具的快照与索引被高频重建；
// 被节流掉的重建由下一个到期的探活补上，变更可见延迟最多一个节拍。
const aggRefreshGap = time.Second

// logQueueSize 是调用日志的异步队列长度。
const logQueueSize = 512

// keyEventQueueSize 是密钥事件的异步队列长度。密钥状态变化是调用路径上的
// 回调，写库必须挪出去（M9）；队列容量按"几十把密钥同时异常"留足余量。
const keyEventQueueSize = 256

// serversTTL 是 server 列表的缓存时长。写路径会同步删缓存，
// 这个 TTL 只用来兜住没有明确写请求的状态变化（周期探活改的是 status）。
const serversTTL = 2 * time.Second

// statsTTL 是调用统计的缓存时长，比列表长一些：它扫整张日志表，随日志
// 增长变慢，而日志本身异步批量落库，这份数字本来就带滞后。
const statsTTL = 5 * time.Second

// serversKey 是 server 列表缓存里唯一那个 key。
const serversKey = "all"

// Service 把配置、连接、聚合三块编排成一个整体，是网关的中枢：
// 所有会影响"工具长什么样"的操作都从这里过，保证 Registry 与 Pool
// 不会各自为政。
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

	// 错峰探活的并发上限（与 RefreshAll 的局部信号量相互独立：
	// 手动全量刷新是显式动作，允许和周期探活短暂并存）。
	probeSem chan struct{}

	// 连败拦截闸：连续探活失败达到阈值后拒绝该上游的调用，
	// 探活不受影响。见 service_block.go。
	gate *blockGate

	// 聚合快照上次重建时刻（UnixNano），refreshAggThrottled 用它节流。
	lastAggRefresh atomic.Int64

	// reloadFn 覆盖 Reload 的实现（见 Options.ReloadFn），生产恒为 nil。
	reloadFn func(context.Context) error

	// last_used_at 的落库节流（见 touchAPIKey）。
	touchMu   sync.Mutex
	touchLast map[string]time.Time
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
	//
	// 它是"写已落库、刷新失败"这条路径唯一的接缝：写路径必须把重载
	// 失败当成运行时状态问题（记 error 日志、缓存照常失效），而不是
	// 把一次已经提交的写判成失败——库里已经有新配置了。只有测试会设置它。
	ReloadFn func(context.Context) error
}

// NewServiceWithCache 用指定的缓存后端构造服务并完成首次聚合装载。
// 首次装载只读已落库的缓存、不触发探活——服务能立刻提供上一次已知的
// 工具列表，哪怕此刻所有下游都还没探过。
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
		store:      st,
		pool:       pool.NewPool(logger),
		reg:        registry.NewRegistry(),
		retr:       retrieval.NewEngine(st, logger),
		log:        logger,
		specMap:    make(map[string]downstream.Spec),
		cache:      opts.Cache,
		servers:    cache.NewTyped[[]servers.Server](opts.Cache, "hub:servers"),
		stats:      cache.NewTyped[logs.CallStats](opts.Cache, "hub:stats"),
		logCh:      make(chan logs.LogEntry, logQueueSize),
		closeCh:    make(chan struct{}),
		keyEventCh: make(chan pool.KeyEvent, keyEventQueueSize),
		probeSem:   make(chan struct{}, probeConcurrency),
		gate:       newBlockGate(opts.BlockAfterFails),
		reloadFn:   opts.ReloadFn,
	}

	s.agg = aggregate.NewAggregator(aggregate.Deps{
		Sources: s.sources,
		SpecOf:  s.specOf,
		Log:     s.enqueueLog,
		Blocked: s.gate.blocked,
	}, s.reg, s.pool, opts.ListMode)

	// 密钥状态变化由连接池回调到这里落库与记日志。用回调而不是让
	// 连接池直接写库：连接池不该知道存储的存在。
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
// 两者必须同步：漏掉任何一边都会让"搜出来的工具在列表里不存在"，
// 或反过来。
func (s *Service) refreshAgg() {
	s.lastAggRefresh.Store(time.Now().UnixNano())
	s.agg.Refresh()
	s.retr.RebuildIndex(context.Background())
	// 增量 embed 重建：语料未变时零 API 成本（hash 比对），
	// 让新增 server/工具、描述变更在下一个刷新周期内自动获得向量。
	// Rebuild 自带重入保护，并发调用直接返回现状。
	if em := s.retr.Embedder(); em != nil {
		go em.Rebuild(context.Background())
	}
}

// refreshAggThrottled 是错峰探活专用的节流版 refreshAgg。
// 相邻两次重建不足 aggRefreshGap 时直接跳过——跳过的这轮变更由下一个
// 到期的探活补上，变更可见延迟最多一个节拍，换来的是几千工具规模下
// 快照与索引不被高频重建。
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
// 判据是"有工具可提供"而不是"所有下游都健康"——后者会让任何一个下游
// 抖动都导致网关未就绪，恰恰违反故障隔离的初衷。
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

// sources 从存储读出聚合输入。
// 每次读库而不维护内存副本：Refresh 是低频操作，本地读是微秒级，
// 多一份缓存就多一处可能与库不一致的状态。
func (s *Service) sources() []registry.ToolSource {
	ctx := context.Background()

	srvs, err := s.store.Servers.List(ctx)
	if err != nil {
		s.log.Error("聚合时读取 server 列表失败", zap.Error(err))
		return nil
	}
	tools, err := s.store.Servers.ListTools(ctx)
	if err != nil {
		s.log.Error("聚合时读取工具缓存失败", zap.Error(err))
		return nil
	}
	cats, err := s.store.Categories.List(ctx)
	if err != nil {
		s.log.Error("聚合时读取分类失败", zap.Error(err))
		return nil
	}
	// 分类只取中文展示名：它不进工具名（对外名 = serverKey__工具名），
	// 只用于展示、过滤与词法检索，所以这里不需要 slug。
	catByID := make(map[string]categories.Category, len(cats))
	for _, c := range cats {
		catByID[c.ID] = c
	}

	// 缓存里的工具分两堆：开关开着的进聚合，人工关掉的只留给管理界面
	// ——否则关掉就再也找不回来，等于没有这个开关。
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
	if sv, err := s.store.Servers.Get(ctx, serverID); err == nil {
		return sv.Spec.Key
	}
	return serverID
}
