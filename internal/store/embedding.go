package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm/clause"
)

// ErrVectorUnsupported 表示当前方言没有向量检索路径。
//
// 共用 schema 里向量存 TEXT（JSON），ANN 只有 PG 侧能做（pgvector
// 强转）；SQLite 侧本函数返回这个错误，上层按降级阶梯退回 keyword/bm25。
var ErrVectorUnsupported = errors.New("当前数据库方言不支持向量检索")

// EmbeddingHit 是一次向量检索的结果。
type EmbeddingHit struct {
	ToolID   string
	Distance float64 // 余弦距离，越小越近
}

// SearchEmbeddings 对已落库的向量做相似度检索（PRD §4.7.4 hybrid 的向量一路）。
//
// 只查指定 model 与维度的向量：混入旧模型向量是错的——维度对不上
// 会直接报错，语义空间不同则是悄悄错。只查启用且可用的 server：
// 此刻调不通的工具排进结果，AI 按结果去调用只会再撞一次错误。
func (s *Store) SearchEmbeddings(ctx context.Context, model string, dims int, query []float64, topK int) ([]EmbeddingHit, error) {
	if s.kind != DialectPostgres {
		return nil, ErrVectorUnsupported
	}
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
		`SELECT e.tool_id AS tool_id,
		        (e.vector::vector <=> ?::vector) AS distance
		   FROM tool_embeddings e
		   JOIN tools t ON t.id = e.tool_id
		   JOIN servers s ON s.id = t.server_id
		  WHERE s.enabled = 1
		    AND s.status IN ('healthy', 'degraded')
		    AND e.model = ? AND e.dims = ?
		  ORDER BY distance
		  LIMIT ?`, string(vec), model, dims, topK).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("向量检索失败: %w", err)
	}
	out := make([]EmbeddingHit, 0, len(rows))
	for _, r := range rows {
		out = append(out, EmbeddingHit{ToolID: r.ToolID, Distance: r.Distance})
	}
	return out, nil
}

// Embedding 是 tool_embeddings 表的一行：某个工具的向量。
//
// 与 toolRow 一对一（ON DELETE CASCADE）：replaceTools 整体替换
// 工具缓存时，旧向量随旧 tool 行自动消失，不需要单独的清理逻辑。
type Embedding struct {
	ToolID     string
	Model      string
	Dims       int
	Vector     []float64
	CorpusHash string
	CreatedAt  time.Time
}

// SaveEmbeddings 批量 upsert 向量，供 RAG 增量重建使用。
//
// 冲突时覆盖 model/dims/vector/corpus_hash：corpus_hash 变了
// 就是"这条语料被重新向量化了"，旧向量没有保留价值。
func (s *Store) SaveEmbeddings(ctx context.Context, es []Embedding) error {
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
			Vector:     jsonOrEmptySlice(e.Vector),
			CorpusHash: e.CorpusHash,
			CreatedMS:  now,
		})
	}

	err := s.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "tool_id"}},
			DoUpdates: clause.AssignmentColumns([]string{
				"model", "dims", "vector", "corpus_hash", "created_at",
			}),
		}).
		CreateInBatches(&rows, 200).Error
	if err != nil {
		return fmt.Errorf("写入工具向量失败(%d 条): %w", len(rows), err)
	}
	return nil
}

// ListEmbeddings 返回全部已有向量，供增量重建时按 corpus_hash 判定
// 哪些工具需要重新向量化。
func (s *Store) ListEmbeddings(ctx context.Context) ([]Embedding, error) {
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

// DeleteEmbeddings 按 tool id 删除向量（全量重建前的清理用）。
func (s *Store) DeleteEmbeddings(ctx context.Context, toolIDs []string) error {
	if len(toolIDs) == 0 {
		return nil
	}
	res := s.db.WithContext(ctx).Where("tool_id IN ?", toolIDs).Delete(&embeddingRow{})
	if res.Error != nil {
		return fmt.Errorf("删除工具向量失败: %w", res.Error)
	}
	return nil
}
