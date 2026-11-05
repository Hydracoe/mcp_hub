package retrieval

import (
	"context"
	"errors"
	"sync"

	"github.com/hydra/mcp-hub/internal/search/client"
	"github.com/hydra/mcp-hub/internal/search/corpus"
	"github.com/hydra/mcp-hub/internal/store"
	"github.com/hydra/mcp-hub/internal/store/embeddings"
	"go.uber.org/zap"
)

/*
embedding 的配置只有一个来源：环境变量（见 cmd/mcp-hub/config.go）。
管理界面不提供入口、库里也不存配置——多一层来源就会多出"改了 .env 却不生效"这类问题。
改配置 = 改 .env 后重启，Reconcile 会在启动时按当前代补齐向量并清掉其它代。
*/

const (
	// autoCategorizeThreshold 是自动应用分类的最低余弦相似度。
	// bge-m3 下相关文本的相似度通常在 0.5~0.8，低于这条线的 top-1
	// 自动应用更像"乱安"，宁可只给建议让用户拍板。
	autoCategorizeThreshold = 0.5
)

// EmbedOptions 是进程提供的 embedding 连接信息，全部来自环境变量。
// APIKey / RerankAPIKey 只存在于进程里，绝不落库、绝不出现在响应里；
// rerank 地址/密钥留空时回落为共用 embed 的。
type EmbedOptions struct {
	BaseURL    string
	APIKey     string
	Model      string
	Dimensions int
	Batch      int
	// CorpusFields 决定哪些字段进语料。零值视为"未配置"并折成内置默认
	// ——语料为空的向量没有检索意义，当作合法配置只会白调一遍接口。
	CorpusFields corpus.Fields
	// RerankModel 为空 = 关闭精排（hybrid 返回 RRF 融合排序）。
	RerankModel string
	// RerankBaseURL 为空 → 用 BaseURL。
	RerankBaseURL string
	// RerankAPIKey 为空 → 用 APIKey。
	RerankAPIKey string
}

// EmbedConfig 是当前生效的 embedding 配置。
// 由进程配置算出后保持不变，不落库、不可热改；model / dimensions / 语料字段
// 三件套构成"当前代"的身份（见 genOf）。
type EmbedConfig struct {
	Model         string
	Dimensions    int
	BaseURL       string
	RerankModel   string
	RerankBaseURL string
	Batch         int
	CorpusFields  corpus.Fields
}

// RebuildState 是重建任务的状态。
type RebuildState string

const (
	RebuildIdle    RebuildState = "idle"
	RebuildRunning RebuildState = "running"
	RebuildDone    RebuildState = "done"
	RebuildFailed  RebuildState = "failed"
	// RebuildSkipped 表示"本次调用没有真正开始重建"：已有一次重建在跑。
	// 它是调用级别的结果而不是生命周期状态（管理器自身仍是 running）；
	// 有了它，重复调用绝不会被误读成"已完成"。
	RebuildSkipped RebuildState = "skipped"
)

// RebuildStatus 是一次向量重建的进度与结果，用于日志与启动自检。
// Ready 是"这一代可用了"的判定：无失败且所有有语料的工具在本代下都有向量，
// Reconcile 用它决定能否清掉上一代；State 为 skipped 时其余字段是在跑那次的快照。
type RebuildStatus struct {
	State     RebuildState
	Total     int
	Done      int
	Failed    int
	Covered   int
	Ready     bool
	Message   string
	UpdatedAt int64
}

// EmbedManager 编排 embedding 与自动分类。
// 不持有工具数据，每次重建都从库里现读——多一份副本只会多一处与库不一致的可能。
type EmbedManager struct {
	store *store.Store
	log   *zap.Logger
	// apiKey 是 embed 的密钥；为空时 embedder 为 nil，检索降级 bm25。
	apiKey string
	// rerankAPIKey 为空表示 rerank 与 embed 用同一把密钥（最常见形态）。
	rerankAPIKey string

	mu       sync.Mutex
	cfg      EmbedConfig
	embedder *client.Embedder
	reranker *client.Reranker
	status   RebuildStatus
	// activeGen 是最近一次"完全就绪"的代：检索的向量一路只查这一代。
	// nil 表示还没有任何就绪的代（首次启动构建中、或换代后构建未竟），
	// 向量分支自动降级——半成品代永远不进检索，代切换因此是原子的。
	activeGen *embeddings.Generation

	catMu   sync.Mutex
	catVecs map[string][]float64
	// catText 记录每个分类向量对应的"名称+描述"：改名/改描述后旧向量必须重算，
	// 否则自动归类会一直用旧语义直到进程重启。
	catText map[string]string
}

// genOf 从配置算出代身份。
func genOf(cfg EmbedConfig) embeddings.Generation {
	return embeddings.Generation{Model: cfg.Model, Dims: cfg.Dimensions, Spec: corpus.SpecString(cfg.CorpusFields)}
}

// NewEmbedManager 用进程配置构造管理器。
//
// 参数里的值（含 cmd 层从环境变量填的默认）就是唯一权威，零值在这里折成内置默认，
// 保证 Config() 读到的永远可用。rerank 不带内置默认：空值表示关闭精排。
func NewEmbedManager(st *store.Store, log *zap.Logger, o EmbedOptions) *EmbedManager {
	if log == nil {
		log = zap.NewNop()
	}

	cfg := EmbedConfig{
		Model:         o.Model,
		Dimensions:    o.Dimensions,
		BaseURL:       o.BaseURL,
		RerankModel:   o.RerankModel,
		RerankBaseURL: o.RerankBaseURL,
		Batch:         o.Batch,
		CorpusFields:  o.CorpusFields,
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = client.DefaultBaseURL
	}
	if cfg.Model == "" {
		cfg.Model = client.DefaultModel
	}
	if cfg.Dimensions <= 0 {
		cfg.Dimensions = client.DefaultDims
	}
	if cfg.Batch <= 0 {
		cfg.Batch = client.DefaultBatch
	}
	if cfg.CorpusFields == (corpus.Fields{}) {
		cfg.CorpusFields = corpus.DefaultFields()
	}

	m := &EmbedManager{
		store:        st,
		log:          log,
		apiKey:       o.APIKey,
		rerankAPIKey: o.RerankAPIKey,
		cfg:          cfg,
		catVecs:      make(map[string][]float64),
		catText:      make(map[string]string),
	}
	m.buildClients()
	return m
}

// buildClients 按当前配置重建 embed/rerank 客户端。调用方持锁。
func (m *EmbedManager) buildClients() {
	m.embedder = nil
	m.reranker = nil
	if m.apiKey == "" || m.cfg.Model == "" {
		return
	}
	// 维度/批次非法时构造失败：embedder 保持 nil，检索降级为词法一路，
	// 并在日志里留下原因。配置错误不该让进程起不来，但必须看得见。
	e, err := client.NewEmbedder(m.apiKey, m.cfg.BaseURL, m.cfg.Model, m.cfg.Dimensions, m.batch())
	if err != nil {
		m.log.Error("embed 客户端构造失败，向量检索已禁用", zap.Error(err))
		return
	}
	m.embedder = e

	// rerank 的凭据与地址可独立于 embed：embed 在 A 家、rerank 在 B 家时
	// 必需。两边同源更常见，所以空值回落而不是必填——少两处配错的机会。
	if m.cfg.RerankModel == "" {
		return
	}
	key := m.rerankAPIKey
	if key == "" {
		key = m.apiKey
	}
	base := m.cfg.RerankBaseURL
	if base == "" {
		base = m.cfg.BaseURL
	}
	m.reranker = client.NewReranker(key, base, m.cfg.RerankModel)
}

// batch 返回当前生效的批次大小。调用方持锁。
func (m *EmbedManager) batch() int { return batchOrDefault(m.cfg.Batch) }

// batchOrDefault 归一化批次大小：≤0 视为"用默认"。
func batchOrDefault(n int) int {
	if n > 0 {
		return n
	}
	return client.DefaultBatch
}

// Config 返回当前生效的配置。
func (m *EmbedManager) Config() EmbedConfig {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cfg
}

// ActiveGeneration 返回检索的向量一路应当查询的代（最近一次完全就绪的代）。
// 返回 nil 表示还没有就绪的代：向量分支应整体跳过而非查询半成品。
func (m *EmbedManager) ActiveGeneration() *embeddings.Generation {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.activeGen
}

// Embedder 返回当前 embed 客户端；未配置密钥时为 nil。
func (m *EmbedManager) Embedder() *client.Embedder {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.embedder
}

// Reranker 返回当前 rerank 客户端；未配置时为 nil。
func (m *EmbedManager) Reranker() *client.Reranker {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.reranker
}

// EmbedQuery 把单条文本变成向量。
func (m *EmbedManager) EmbedQuery(ctx context.Context, text string) ([]float64, error) {
	m.mu.Lock()
	e := m.embedder
	m.mu.Unlock()
	if e == nil {
		return nil, errors.New("未配置 embedding 服务（缺 MCPHUB_EMBED_API_KEY）")
	}
	vs, err := e.Embed(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	return vs[0], nil
}

// Status 返回最近一次（或进行中的）重建进度。
func (m *EmbedManager) Status() RebuildStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.status
}
