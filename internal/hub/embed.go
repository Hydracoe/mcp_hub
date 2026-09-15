package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hydra/mcp-hub/internal/search"
	"github.com/hydra/mcp-hub/internal/store"
	"go.uber.org/zap"
)

const (
	// embedConfigKey 是 app_config 里 embedding 配置的 key。
	embedConfigKey = "embed"

	// autoCategorizeThreshold 是自动应用分类的最低余弦相似度。
	//
	// bge-m3 下相关文本的相似度通常在 0.5~0.8；低于这条线的 top-1
	// 自动应用更像"乱安"，宁可只给建议让用户拍板。
	autoCategorizeThreshold = 0.5
)

// EmbedOptions 是进程提供的 embedding 连接信息。
//
// APIKey 只存在于进程里（flag / 环境变量），绝不落库：
// 配置表里的 BaseURL/Model 是给界面展示的，密钥不是。
type EmbedOptions struct {
	BaseURL     string
	APIKey      string
	Model       string
	Dimensions  int
	RerankModel string
}

// EmbedConfig 是 embedding 配置里持久化、可热改的部分（PRD §4.7.5）。
//
// 改 model / dimensions / 语料字段都会让旧向量失效（不同模型不同语义空间），
// 因此这三项的变化会触发一次重建；BaseURL 与 RerankModel 不触发。
type EmbedConfig struct {
	Model        string              `json:"model"`
	Dimensions   int                 `json:"dimensions"`
	BaseURL      string              `json:"baseUrl"`
	RerankModel  string              `json:"rerankModel"`
	CorpusFields search.CorpusFields `json:"corpusFields"`
}

// RebuildState 是重建任务的状态。
type RebuildState string

const (
	RebuildIdle    RebuildState = "idle"
	RebuildRunning RebuildState = "running"
	RebuildDone    RebuildState = "done"
	RebuildFailed  RebuildState = "failed"
)

// RebuildStatus 是重建任务的进度，供 /api/embed-config/rebuild 查询。
type RebuildStatus struct {
	State     RebuildState `json:"state"`
	Total     int          `json:"total"`
	Done      int          `json:"done"`
	Failed    int          `json:"failed"`
	Message   string       `json:"message,omitempty"`
	UpdatedAt int64        `json:"updatedAt"`
}

// EmbedManager 编排 embedding 与自动分类（PRD §4.7.5 / §4.7.3）。
//
// 它不持有工具数据——每次重建都从库里现读。持有数据副本会多出一处
// 与库不一致的可能状态，而重建是低频操作，读库的代价可以忽略。
type EmbedManager struct {
	store  *store.Store
	log    *zap.Logger
	apiKey string

	mu       sync.Mutex
	cfg      EmbedConfig
	embedder *search.Embedder
	reranker *search.Reranker
	status   RebuildStatus

	catMu   sync.Mutex
	catVecs map[string][]float64
}

// NewEmbedManager 从 app_config 载入已持久化的配置；
// 首次启动（无配置）时把进程 flag 给的值落库，让管理接口即刻可用。
func NewEmbedManager(st *store.Store, log *zap.Logger, o EmbedOptions) *EmbedManager {
	if log == nil {
		log = zap.NewNop()
	}

	cfg := EmbedConfig{
		Model:        o.Model,
		Dimensions:   o.Dimensions,
		BaseURL:      o.BaseURL,
		RerankModel:  o.RerankModel,
		CorpusFields: search.DefaultCorpusFields(),
	}

	ctx := context.Background()
	raw, err := st.GetConfig(ctx, embedConfigKey)
	switch {
	case err != nil:
		log.Warn("读取 embedding 配置失败，使用进程默认", zap.Error(err))
	case strings.TrimSpace(raw) != "":
		var saved EmbedConfig
		if json.Unmarshal([]byte(raw), &saved) == nil {
			if saved.Model != "" {
				cfg.Model = saved.Model
			}
			if saved.Dimensions > 0 {
				cfg.Dimensions = saved.Dimensions
			}
			if saved.BaseURL != "" {
				cfg.BaseURL = saved.BaseURL
			}
			if saved.RerankModel != "" {
				cfg.RerankModel = saved.RerankModel
			}
			if saved.CorpusFields != (search.CorpusFields{}) {
				cfg.CorpusFields = saved.CorpusFields
			}
		} else {
			log.Warn("embedding 配置不是合法 JSON，使用进程默认")
		}
	default:
		if b, merr := json.Marshal(cfg); merr == nil {
			if serr := st.SetConfig(ctx, embedConfigKey, string(b)); serr != nil {
				log.Warn("持久化默认 embedding 配置失败", zap.Error(serr))
			}
		}
	}

	m := &EmbedManager{
		store:   st,
		log:     log,
		apiKey:  o.APIKey,
		cfg:     cfg,
		catVecs: make(map[string][]float64),
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
	m.embedder = search.NewEmbedder(m.apiKey, m.cfg.BaseURL, m.cfg.Model, search.DefaultEmbedBatch)
	if m.cfg.RerankModel != "" {
		m.reranker = search.NewReranker(m.apiKey, m.cfg.BaseURL, m.cfg.RerankModel)
	}
}

// Config 返回当前生效的配置。
func (m *EmbedManager) Config() EmbedConfig {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cfg
}

// Embedder 返回当前 embed 客户端；未配置密钥时为 nil。
func (m *EmbedManager) Embedder() *search.Embedder {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.embedder
}

// Reranker 返回当前 rerank 客户端；未配置时为 nil。
func (m *EmbedManager) Reranker() *search.Reranker {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.reranker
}

// UpdateConfig 更新并持久化配置，返回是否需要重建向量。
func (m *EmbedManager) UpdateConfig(ctx context.Context, cfg EmbedConfig) (bool, error) {
	if strings.TrimSpace(cfg.Model) == "" {
		return false, errors.New("model 不能为空")
	}
	if cfg.Dimensions <= 0 {
		return false, errors.New("dimensions 必须大于 0")
	}
	cur := m.Config()
	if strings.TrimSpace(cfg.BaseURL) == "" {
		cfg.BaseURL = cur.BaseURL
	}
	if cfg.RerankModel == "" {
		cfg.RerankModel = cur.RerankModel
	}
	if cfg.CorpusFields == (search.CorpusFields{}) {
		cfg.CorpusFields = search.DefaultCorpusFields()
	}

	m.mu.Lock()
	changed := cfg.Model != m.cfg.Model ||
		cfg.Dimensions != m.cfg.Dimensions ||
		cfg.CorpusFields != m.cfg.CorpusFields
	old := m.cfg
	m.cfg = cfg
	m.buildClients()
	m.mu.Unlock()

	b, err := json.Marshal(cfg)
	if err != nil {
		return false, err
	}
	if err := m.store.SetConfig(ctx, embedConfigKey, string(b)); err != nil {
		// 持久化失败就把配置回滚：内存里生效而库里是旧的，
		// 重启后会"配置丢失"，比这一次改配置失败糟糕得多。
		m.mu.Lock()
		m.cfg = old
		m.buildClients()
		m.mu.Unlock()
		return false, fmt.Errorf("持久化 embedding 配置失败: %w", err)
	}

	if changed {
		m.log.Info("embedding 配置已变更，需要重建向量",
			zap.String("model", cfg.Model), zap.Int("dims", cfg.Dimensions))
	}
	return changed, nil
}

// EmbedQuery 把单条文本变成向量。
func (m *EmbedManager) EmbedQuery(ctx context.Context, text string) ([]float64, error) {
	m.mu.Lock()
	e := m.embedder
	m.mu.Unlock()
	if e == nil {
		return nil, errors.New("未配置 embedding 服务（缺少 --embed-api-key）")
	}
	vs, err := e.Embed(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	return vs[0], nil
}

// Rebuild 增量（重）嵌入全部工具语料（PRD §4.7.5）。
//
// corpus_hash 没变的跳过，成本与变更量成正比；单批失败保留旧向量并继续，
// 重建失败不抹掉已有能力。同一时刻只有一个重建在跑，重复调用直接返回进度。
func (m *EmbedManager) Rebuild(ctx context.Context) RebuildStatus {
	m.mu.Lock()
	if m.status.State == RebuildRunning {
		st := m.status
		m.mu.Unlock()
		return st
	}
	e := m.embedder
	cfg := m.cfg
	m.status = RebuildStatus{State: RebuildRunning, UpdatedAt: time.Now().UnixMilli()}
	m.mu.Unlock()

	if e == nil {
		return m.finish(RebuildFailed, 0, 0, 0, "未配置 embedding 服务（缺少 --embed-api-key）")
	}

	docs, err := collectToolDocs(ctx, m.store, cfg.CorpusFields)
	if err != nil {
		return m.finish(RebuildFailed, 0, 0, 0, fmt.Sprintf("读取工具语料失败: %v", err))
	}

	existing, err := m.store.ListEmbeddings(ctx)
	if err != nil {
		return m.finish(RebuildFailed, 0, 0, 0, fmt.Sprintf("读取已有向量失败: %v", err))
	}
	hashOf := make(map[string]string, len(existing))
	for _, x := range existing {
		hashOf[x.ToolID] = x.CorpusHash
	}

	live := make(map[string]bool, len(docs))
	pending := make([]toolDoc, 0, len(docs))
	for _, d := range docs {
		if strings.TrimSpace(d.Corpus) == "" {
			continue // 无语料不参与 embed：空串的向量没有检索意义
		}
		live[d.ToolID] = true
		if hashOf[d.ToolID] != search.CorpusHash(d.Corpus, cfg.Model, cfg.Dimensions) {
			pending = append(pending, d)
		}
	}

	// 清掉已不存在（或语料变空）的工具的旧向量。
	stale := make([]string, 0)
	for id := range hashOf {
		if !live[id] {
			stale = append(stale, id)
		}
	}
	if err := m.store.DeleteEmbeddings(ctx, stale); err != nil {
		m.log.Warn("清理失效向量失败", zap.Error(err))
	}

	m.progress(len(pending), 0, 0, fmt.Sprintf("共 %d 个工具有语料，其中 %d 个需要重新向量化", len(live), len(pending)))

	embedded, failed := 0, 0
	const batch = search.DefaultEmbedBatch
	for start := 0; start < len(pending); start += batch {
		end := start + batch
		if end > len(pending) {
			end = len(pending)
		}
		texts := make([]string, 0, end-start)
		for _, d := range pending[start:end] {
			texts = append(texts, d.Corpus)
		}

		vecs, err := e.Embed(ctx, texts)
		if err != nil {
			failed += end - start
			m.log.Warn("一批 embedding 失败，保留旧向量",
				zap.Int("count", end-start), zap.Error(err))
			m.progress(len(pending), embedded, failed, fmt.Sprintf("已向量化 %d/%d（%d 批失败）", end, len(pending), failed))
			continue
		}

		es := make([]store.Embedding, 0, len(vecs))
		for i, v := range vecs {
			d := pending[start+i]
			es = append(es, store.Embedding{
				ToolID:     d.ToolID,
				Model:      cfg.Model,
				Dims:       cfg.Dimensions,
				Vector:     v,
				CorpusHash: search.CorpusHash(d.Corpus, cfg.Model, cfg.Dimensions),
				CreatedAt:  time.Now(),
			})
		}
		if err := m.store.SaveEmbeddings(ctx, es); err != nil {
			failed += len(es)
			m.log.Warn("向量写库失败，保留旧向量", zap.Int("count", len(es)), zap.Error(err))
			continue
		}
		embedded += len(es)
		m.progress(len(pending), embedded, failed, fmt.Sprintf("已向量化 %d/%d", end, len(pending)))
	}

	m.progress(len(pending), embedded, failed, "")
	if failed > 0 {
		return m.finish(RebuildFailed, len(pending), embedded, failed,
			fmt.Sprintf("重建完成：%d 成功，%d 失败（失败项保留旧向量）", embedded, failed))
	}
	return m.finish(RebuildDone, len(pending), embedded, 0,
		fmt.Sprintf("重建完成：新向量化 %d 个工具", embedded))
}

// progress 更新运行中进度（不清空状态）。
func (m *EmbedManager) progress(total, done, failed int, msg string) {
	m.mu.Lock()
	m.status.Total = total
	m.status.Done = done
	m.status.Failed = failed
	if msg != "" {
		m.status.Message = msg
	}
	m.status.UpdatedAt = time.Now().UnixMilli()
	m.mu.Unlock()
}

// finish 落定终态并返回。
func (m *EmbedManager) finish(state RebuildState, total, done, failed int, msg string) RebuildStatus {
	m.mu.Lock()
	m.status = RebuildStatus{
		State:     state,
		Total:     total,
		Done:      done,
		Failed:    failed,
		Message:   msg,
		UpdatedAt: time.Now().UnixMilli(),
	}
	st := m.status
	m.mu.Unlock()
	return st
}

// Status 返回最近一次（或进行中的）重建进度。
func (m *EmbedManager) Status() RebuildStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.status
}

// SuggestedCategory 是自动归类建议的一项。
type SuggestedCategory struct {
	ID    string  `json:"id"`
	Name  string  `json:"name"`
	Score float64 `json:"score"`
}

// RefreshCategoryVectors 把分类的名称+描述嵌入进内存缓存。
//
// 不持久化：分类数量少，启动时重嵌一遍比多一张表划算；
// 失败保留旧缓存，自动归类是辅助能力，不该被它拖死。
func (m *EmbedManager) RefreshCategoryVectors(ctx context.Context) {
	m.mu.Lock()
	e := m.embedder
	m.mu.Unlock()
	if e == nil {
		return
	}

	cats, err := m.store.ListCategories(ctx)
	if err != nil {
		return
	}

	var missing []store.Category
	for _, c := range cats {
		m.catMu.Lock()
		_, ok := m.catVecs[c.ID]
		m.catMu.Unlock()
		if !ok {
			missing = append(missing, c)
		}
	}
	if len(missing) == 0 {
		return
	}

	texts := make([]string, 0, len(missing))
	for _, c := range missing {
		texts = append(texts, strings.TrimSpace(c.Name+" "+c.Description))
	}
	vecs, err := e.Embed(ctx, texts)
	if err != nil {
		m.log.Warn("分类向量化失败，保留旧缓存", zap.Error(err))
		return
	}
	m.catMu.Lock()
	for i, c := range missing {
		m.catVecs[c.ID] = vecs[i]
	}
	m.catMu.Unlock()
}

// AutoCategorize 用相似度为给定文本建议分类（PRD §4.7.3）。
//
// applied 是应当自动应用的 top-1（相似度过线才给，否则 nil）；
// suggestions 是完整的 top-3 排序，无论 applied 是否为 nil 都返回，
// 让界面/响应始终有"备选"可展示。embedding 服务不可用时返回空，调用方优雅跳过。
func (m *EmbedManager) AutoCategorize(ctx context.Context, text string) (applied *store.Category, suggestions []SuggestedCategory, err error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, nil, nil
	}

	m.RefreshCategoryVectors(ctx)

	m.catMu.Lock()
	vecs := make(map[string][]float64, len(m.catVecs))
	for k, v := range m.catVecs {
		vecs[k] = v
	}
	m.catMu.Unlock()
	if len(vecs) == 0 {
		return nil, nil, nil
	}

	q, err := m.EmbedQuery(ctx, text)
	if err != nil {
		return nil, nil, err
	}

	cats, err := m.store.ListCategories(ctx)
	if err != nil {
		return nil, nil, err
	}

	byID := make(map[string]store.Category, len(cats))
	scores := make([]SuggestedCategory, 0, len(cats))
	for _, c := range cats {
		v, ok := vecs[c.ID]
		if !ok {
			continue
		}
		byID[c.ID] = c
		scores = append(scores, SuggestedCategory{ID: c.ID, Name: c.Name, Score: search.Cosine(q, v)})
	}
	sort.Slice(scores, func(i, j int) bool {
		if scores[i].Score != scores[j].Score {
			return scores[i].Score > scores[j].Score
		}
		return scores[i].ID < scores[j].ID
	})
	if len(scores) > 3 {
		scores = scores[:3]
	}

	if len(scores) > 0 && scores[0].Score >= autoCategorizeThreshold {
		c := byID[scores[0].ID]
		applied = &c
	}
	return applied, scores, nil
}
