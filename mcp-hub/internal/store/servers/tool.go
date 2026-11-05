package servers

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm/clause"
)

// toolRow 是 tools 表的一行，即某个 server 的工具缓存。
type toolRow struct {
	ID          string `gorm:"column:id;primaryKey"`
	ServerID    string `gorm:"column:server_id"`
	Name        string `gorm:"column:name"`
	ExposedName string `gorm:"column:exposed_name"`
	Description string `gorm:"column:description"`
	InputSchema string `gorm:"column:input_schema"` // JSON Schema
	Enabled     int64  `gorm:"column:enabled"`
	CachedMS    int64  `gorm:"column:cached_at"`
}

func (toolRow) TableName() string { return "tools" }

func (r toolRow) toTool() Tool {
	return Tool{
		ID:          r.ID,
		ServerID:    r.ServerID,
		Name:        r.Name,
		ExposedName: r.ExposedName,
		Description: r.Description,
		InputSchema: r.InputSchema,
		Enabled:     r.Enabled != 0,
		CachedAt:    time.UnixMilli(r.CachedMS),
	}
}

// Tool 是 tools 表的一行，即某个 server 的工具缓存。
type Tool struct {
	ID          string
	ServerID    string
	Name        string // 下游原始名
	ExposedName string
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

// pendingEnableCond 是"探活新发现、默认禁用、从未被人工碰过"的判据。
//
// 判断依据是 tool_changes 里没有该工具的人工记录：人工启用或人工禁用过
// 一次，就视为已有决定，不再计入告警。
// （边界：上游删了同名工具再重新加回来，旧的人工记录仍会让新工具免于
// 告警——发生概率低，误报的代价远小于漏报。）
//
// 抽成常量而不是各写一遍：计数与明细两条查询必须同一个判据，
// 否则"卡片说有 3 个、点进去只有 2 个"这种对不上会一直有人报。
const pendingEnableCond = `t.enabled = 0
	AND NOT EXISTS (
		SELECT 1 FROM tool_changes c
		WHERE c.server_id = t.server_id
			AND c.tool_name = t.name
			AND c.actor = 'admin'
	)`

// CountPendingEnable 统计待人工决定的工具数。
func (s *Store) CountPendingEnable(ctx context.Context) (int64, error) {
	var n int64
	err := s.db.WithContext(ctx).
		Raw("SELECT count(*) FROM tools t WHERE " + pendingEnableCond).Scan(&n).Error
	if err != nil {
		return 0, fmt.Errorf("统计待启用工具失败: %w", err)
	}
	return n, nil
}

// PendingKey 是待启用明细的集合键：一个 server 下工具名唯一，
// 拼成一个字符串方便直接查表。
func PendingKey(serverID, toolName string) string { return serverID + "\x00" + toolName }

// PendingEnableKeys 返回待人工决定的工具集合，供 /api/tools 逐条打标。
//
// 与 CountPendingEnable 同一判据，保证"概览里的数字"与"工具页筛出来的
// 条数"永远一致。
func (s *Store) PendingEnableKeys(ctx context.Context) (map[string]bool, error) {
	var rows []struct {
		ServerID string `gorm:"column:server_id"`
		Name     string `gorm:"column:name"`
	}
	err := s.db.WithContext(ctx).
		Raw("SELECT t.server_id, t.name FROM tools t WHERE " + pendingEnableCond).
		Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("查询待启用工具失败: %w", err)
	}
	out := make(map[string]bool, len(rows))
	for _, r := range rows {
		out[PendingKey(r.ServerID, r.Name)] = true
	}
	return out, nil
}
