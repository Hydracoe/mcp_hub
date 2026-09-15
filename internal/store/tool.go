package store

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm/clause"
)

// Tool 是 tools 表的一行，即某个 server 的工具缓存。
type Tool struct {
	ID          string
	ServerID    string
	Name        string // 下游原始名
	ExposedName string // serverKey__name
	Description string
	InputSchema string
	Enabled     bool
	CachedAt    time.Time
}

// ListTools 返回聚合后的全部工具。
func (s *Store) ListTools(ctx context.Context) ([]Tool, error) {
	return s.listTools(ctx, "", "exposed_name")
}

// ListToolsByServer 返回某个 server 缓存的工具，按原始名排序。
func (s *Store) ListToolsByServer(ctx context.Context, serverID string) ([]Tool, error) {
	return s.listTools(ctx, serverID, "name")
}

func (s *Store) listTools(ctx context.Context, serverID, orderBy string) ([]Tool, error) {
	tx := s.db.WithContext(ctx).Model(&toolRow{})
	if serverID != "" {
		tx = tx.Where("server_id = ?", serverID)
	}

	var rows []toolRow
	if err := tx.Order(clause.OrderByColumn{Column: clause.Column{Name: orderBy}}).
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("查询工具失败: %w", err)
	}

	out := make([]Tool, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.toTool())
	}
	return out, nil
}
