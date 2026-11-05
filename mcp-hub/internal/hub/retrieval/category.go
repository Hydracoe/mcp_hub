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

// RefreshCategoryVectors 把分类的名称+描述嵌入进内存缓存，不持久化；失败保留旧缓存。
// 缓存按"名称+描述"文本判新旧，而不只看 id：改名/改描述后旧向量必须重算，
// 否则自动归类会用一份已经不存在的语义做决定。
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

	texts := make(map[string]string, len(cats))
	for _, c := range cats {
		texts[c.ID] = strings.TrimSpace(c.Name + " " + c.Description)
	}

	var missing []categories.Category
	m.catMu.Lock()
	for _, c := range cats {
		if m.catText[c.ID] != texts[c.ID] {
			missing = append(missing, c)
		}
	}
	// 已删除的分类不再参与比较：不清理的话缓存会随删除操作只增不减。
	for id := range m.catText {
		if _, ok := texts[id]; !ok {
			delete(m.catText, id)
			delete(m.catVecs, id)
		}
	}
	m.catMu.Unlock()
	if len(missing) == 0 {
		return
	}

	textList := make([]string, 0, len(missing))
	for _, c := range missing {
		textList = append(textList, texts[c.ID])
	}
	vecs, err := e.Embed(ctx, textList)
	if err != nil {
		m.log.Warn("分类向量化失败，保留旧缓存", zap.Error(err))
		return
	}
	// 缓存单位向量：查询向量也归一化后，相似度退化成一次点积。
	// 归一化失败（空向量/零向量）的分类不缓存——其相似度没有定义，存进去只会得 0 分。
	m.catMu.Lock()
	for i, c := range missing {
		unit, err := rank.Normalize(vecs[i])
		if err != nil {
			m.log.Warn("分类向量无法归一化，跳过该分类",
				zap.String("category", c.ID), zap.Error(err))
			continue
		}
		m.catVecs[c.ID] = unit
		m.catText[c.ID] = texts[c.ID]
	}
	m.catMu.Unlock()
}

// AutoCategorize 用相似度为给定文本建议分类。
//
// applied 是相似度过线的 top-1（否则 nil）；suggestions 是完整 top-3，始终返回；
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
			// 维度对不上说明缓存里的分类向量与当前查询向量不是同一代，
			// 不可比必须报出来：静默算 0 分会让自动分类无声失效。
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
