package retrieval

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/hydra/mcp-hub/internal/search/rank"
	"github.com/hydra/mcp-hub/internal/store/categories"
	"go.uber.org/zap"
)

// SuggestedCategory 是自动归类建议的一项。
type SuggestedCategory struct {
	ID    string  `json:"id"`
	Name  string  `json:"name"`
	Score float64 `json:"score"`
}

// RefreshCategoryVectors 把分类的名称+描述嵌入进内存缓存。
// 不持久化：分类数量少，启动时重嵌一遍比多一张表划算。失败保留旧缓存
// ——自动归类是辅助能力，不该被它拖死。
func (m *EmbedManager) RefreshCategoryVectors(ctx context.Context) {
	m.mu.Lock()
	e := m.embedder
	m.mu.Unlock()
	if e == nil {
		return
	}

	cats, err := m.store.Categories.List(ctx)
	if err != nil {
		return
	}

	var missing []categories.Category
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
	// 缓存单位向量：查询向量也归一化后，相似度就退化成一次点积，
	// 不必每跟一个分类比一次就重算两边的范数。
	// 归一化失败（空向量/零向量）的分类直接不缓存——它的相似度没有定义，
	// 存进去只会让所有查询都拿到 0 分。
	m.catMu.Lock()
	for i, c := range missing {
		unit, err := rank.Normalize(vecs[i])
		if err != nil {
			m.log.Warn("分类向量无法归一化，跳过该分类",
				zap.String("category", c.ID), zap.Error(err))
			continue
		}
		m.catVecs[c.ID] = unit
	}
	m.catMu.Unlock()
}

// AutoCategorize 用相似度为给定文本建议分类（PRD §4.7.3）。
//
// applied 是应当自动应用的 top-1（相似度过线才有，否则 nil）；suggestions
// 是完整 top-3，无论 applied 是否为 nil 都返回，让响应始终有备选可展示。
// embedding 服务不可用时返回空，调用方优雅跳过。
func (m *EmbedManager) AutoCategorize(ctx context.Context, text string) (applied *categories.Category, suggestions []SuggestedCategory, err error) {
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
	// 查询向量归一化一次，之后与缓存的单位向量只做点积。
	qn, err := rank.Normalize(q)
	if err != nil {
		return nil, nil, fmt.Errorf("归一化查询向量: %w", err)
	}

	cats, err := m.store.Categories.List(ctx)
	if err != nil {
		return nil, nil, err
	}

	byID := make(map[string]categories.Category, len(cats))
	scores := make([]SuggestedCategory, 0, len(cats))
	for _, c := range cats {
		v, ok := vecs[c.ID]
		if !ok {
			continue
		}
		score, err := rank.Dot(qn, v)
		if err != nil {
			// 维度对不上说明缓存里的分类向量与当前查询向量不是同一代。
			// 这里曾经静默算成 0 分，结果是自动分类无声失效（applied 恒为
			// nil），排查只能靠"结果看起来不对"。不可比就要报出来。
			return nil, nil, fmt.Errorf("分类 %s 的向量与查询向量不可比: %w", c.ID, err)
		}
		byID[c.ID] = c
		scores = append(scores, SuggestedCategory{ID: c.ID, Name: c.Name, Score: score})
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
