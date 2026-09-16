// Package embeddings 是 tool_embeddings 表的持久化：工具向量与多代管理。
package embeddings

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/hydra/mcp-hub/internal/store/storeutil"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// embeddingRow 是 tool_embeddings 表的一行（持久化形态）。
// vector 存 TEXT（JSON 数组）：表的读写不依赖 pgvector 装没装上，缺扩展时检索链路整体降级而库照常可用。
// 主键是完整代身份 (tool_id, model, dims, spec)：不同代向量可并存，支撑热切换。
type embeddingRow struct {
	ToolID     string `gorm:"column:tool_id;primaryKey"`
	Model      string `gorm:"column:model;primaryKey"`
	Dims       int64  `gorm:"column:dims;primaryKey"`
	Spec       string `gorm:"column:spec;primaryKey"`
	Vector     string `gorm:"column:vector"` // JSON array
	CorpusHash string `gorm:"column:corpus_hash"`
	CreatedMS  int64  `gorm:"column:created_at"`
}

func (embeddingRow) TableName() string { return "tool_embeddings" }

func (r embeddingRow) toEmbedding() Embedding {
	e := Embedding{
		ToolID:     r.ToolID,
		Model:      r.Model,
		Dims:       int(r.Dims),
		Spec:       r.Spec,
		CorpusHash: r.CorpusHash,
		CreatedAt:  time.UnixMilli(r.CreatedMS),
	}
	if err := json.Unmarshal([]byte(r.Vector), &e.Vector); err != nil {
		e.Vector = nil
	}
	return e
}

// Embedding 是 tool_embeddings 表的一行（领域形态）：某个工具在某一代下的向量。
// 与 tool 行级联：工具缓存整体替换时旧向量自动消失。
type Embedding struct {
	ToolID     string
	Model      string
	Dims       int
	Spec       string
	Vector     []float64
	CorpusHash string
	CreatedAt  time.Time
}

// EmbeddingHit 是一次向量检索的结果。
type EmbeddingHit struct {
	ToolID   string
	Distance float64 // 余弦距离，越小越近
}

// Generation 标识"一代"向量：身份三件套 model + dims + spec（语料字段组合编码）。
// 任一变化即新代——新代整代建好再原子切换（热切换），两代行可并存。
type Generation struct {
	Model string
	Dims  int
	Spec  string
}

// Store 是 tool_embeddings 表的读写入口。
type Store struct {
	db *gorm.DB
}

// New 构造 embeddings 包的读写入口。
func New(db *gorm.DB) *Store { return &Store{db: db} }

// Search 对已落库的向量做相似度检索（hybrid 检索的向量一路）。
// 只查指定代：维度对不上会直接报错，语义空间不同则是悄悄错。
// 只查启用且可用的 server：此刻调不通的工具排进结果，AI 只会再撞一次错。
// 三表连接按表名写原生 SQL——为了一个只读查询引入跨包依赖不值得。
func (s *Store) Search(ctx context.Context, model string, dims int, spec string, query []float64, topK int) ([]EmbeddingHit, error) {
	if topK <= 0 {
		topK = 20
	}
	vec, err := json.Marshal(query)
	if err != nil {
		return nil, fmt.Errorf("序列化查询向量失败: %w", err)
	}

	var rows []struct {
		ToolID   string
		Distance float64
	}
	err = s.db.WithContext(ctx).Raw(
		`SELECT e.tool_id AS tool_id, (e.vector::vector <=> ?::vector) AS distance
		FROM tool_embeddings e
		JOIN tools t ON t.id = e.tool_id
		JOIN servers s ON s.id = t.server_id
		WHERE s.enabled = 1
		AND s.status IN ('healthy', 'degraded')
		AND e.model = ? AND e.dims = ? AND e.spec = ?
		ORDER BY distance
		LIMIT ?`, string(vec), model, dims, spec, topK).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("向量检索失败: %w", err)
	}
	out := make([]EmbeddingHit, 0, len(rows))
	for _, r := range rows {
		out = append(out, EmbeddingHit{ToolID: r.ToolID, Distance: r.Distance})
	}
	return out, nil
}

// Save 批量 upsert 向量，供 RAG 增量重建使用。
// 冲突键是完整代身份；冲突时覆盖 vector/corpus_hash（语料被重新向量化，
// 旧向量没有保留价值）。
func (s *Store) Save(ctx context.Context, es []Embedding) error {
	if len(es) == 0 {
		return nil
	}

	now := time.Now().UnixMilli()
	rows := make([]embeddingRow, 0, len(es))
	for _, e := range es {
		rows = append(rows, embeddingRow{
			ToolID:     e.ToolID,
			Model:      e.Model,
			Dims:       int64(e.Dims),
			Spec:       e.Spec,
			Vector:     storeutil.JSONOrEmptySlice(e.Vector),
			CorpusHash: e.CorpusHash,
			CreatedMS:  now,
		})
	}

	err := s.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns: []clause.Column{
				{Name: "tool_id"}, {Name: "model"}, {Name: "dims"}, {Name: "spec"},
			},
			DoUpdates: clause.AssignmentColumns([]string{
				"vector", "corpus_hash", "created_at",
			}),
		}).
		CreateInBatches(&rows, 200).Error
	if err != nil {
		return fmt.Errorf("写入工具向量失败(%d 条): %w", len(rows), err)
	}
	return nil
}

// List 返回全部已有向量，供增量重建时按 corpus_hash 判定
// 哪些工具需要重新向量化。
func (s *Store) List(ctx context.Context) ([]Embedding, error) {
	var rows []embeddingRow
	err := s.db.WithContext(ctx).Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("查询工具向量失败: %w", err)
	}
	out := make([]Embedding, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.toEmbedding())
	}
	return out, nil
}

// Delete 按 tool id 删除该工具**所有代**的向量（工具消失后的清理）。
func (s *Store) Delete(ctx context.Context, toolIDs []string) error {
	if len(toolIDs) == 0 {
		return nil
	}
	res := s.db.WithContext(ctx).Where("tool_id IN ?", toolIDs).Delete(&embeddingRow{})
	if res.Error != nil {
		return fmt.Errorf("删除工具向量失败: %w", res.Error)
	}
	return nil
}

// DeleteStaleGenerations 删除 keep 之外的所有代（热切换后的 GC）。
// 保留集通常是 {当前代, 上一代}：上一代留到下次切换，支撑一键回滚。
func (s *Store) DeleteStaleGenerations(ctx context.Context, keep ...Generation) error {
	if len(keep) == 0 {
		// GORM 拒绝无 WHERE 的整表 Delete，清空走原生 SQL。
		res := s.db.WithContext(ctx).Exec("DELETE FROM tool_embeddings")
		if res.Error != nil {
			return fmt.Errorf("清理旧代向量失败: %w", res.Error)
		}
		return nil
	}

	conds := make([]string, 0, len(keep))
	args := make([]any, 0, len(keep)*3)
	for _, g := range keep {
		conds = append(conds, "(model = ? AND dims = ? AND spec = ?)")
		args = append(args, g.Model, g.Dims, g.Spec)
	}
	res := s.db.WithContext(ctx).
		Where("NOT ("+strings.Join(conds, " OR ")+")", args...).
		Delete(&embeddingRow{})
	if res.Error != nil {
		return fmt.Errorf("清理旧代向量失败: %w", res.Error)
	}
	return nil
}

// Count 统计某一代已有的向量行数
// （热切换就绪判定与回滚可行性判定都用它）。
func (s *Store) Count(ctx context.Context, model string, dims int, spec string) (int, error) {
	var n int64
	err := s.db.WithContext(ctx).Model(&embeddingRow{}).
		Where("model = ? AND dims = ? AND spec = ?", model, dims, spec).
		Count(&n).Error
	if err != nil {
		return 0, fmt.Errorf("统计向量行数失败: %w", err)
	}
	return int(n), nil
}
