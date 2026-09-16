// Package toolchanges 是 tool_changes 表的读写：工具的增删改与启停留痕。
//
// 记录只追加、不改写：它是"什么时候发生过什么"的历史，任何后续状态
// 变化都该新增一行而不是回改旧行。
package toolchanges

import (
	"context"
	"fmt"
	"time"

	"github.com/hydra/mcp-hub/internal/store/storeutil"

	"gorm.io/gorm"
)

// 动作类型。
const (
	ActionAdded    = "added"    // 探活新发现的工具
	ActionRemoved  = "removed"  // 上游已删除
	ActionUpdated  = "updated"  // 上游描述 / 参数 schema 发生变化
	ActionEnabled  = "enabled"  // 人工启用
	ActionDisabled = "disabled" // 人工禁用
)

// 操作者。
const (
	ActorSystem = "system" // 探活对账自动写入
	ActorAdmin  = "admin"  // 管理接口人工操作
)

// changeRow 是 tool_changes 表的一行。
type changeRow struct {
	ID        string `gorm:"column:id;primaryKey"`
	ServerID  string `gorm:"column:server_id"`
	ServerKey string `gorm:"column:server_key"`
	ToolName  string `gorm:"column:tool_name"`
	Action    string `gorm:"column:action"`
	Detail    string `gorm:"column:detail"`
	Actor     string `gorm:"column:actor"`
	TS        int64  `gorm:"column:ts"`
}

func (changeRow) TableName() string { return "tool_changes" }

// Change 是一条工具变更记录（领域形态）。
type Change struct {
	ID        string
	ServerID  string
	ServerKey string
	ToolName  string
	Action    string
	Detail    string
	Actor     string
	TS        time.Time
}

func (r changeRow) toChange() Change {
	return Change{
		ID:        r.ID,
		ServerID:  r.ServerID,
		ServerKey: r.ServerKey,
		ToolName:  r.ToolName,
		Action:    r.Action,
		Detail:    r.Detail,
		Actor:     r.Actor,
		TS:        time.UnixMilli(r.TS),
	}
}

// Store 是 tool_changes 表的读写入口。
type Store struct {
	db *gorm.DB
}

// New 构造读写入口。
func New(db *gorm.DB) *Store { return &Store{db: db} }

// InsertTx 在给定事务内追加一条记录。探活对账要与工具缓存替换同事务，
// 不能留下"工具已删但删除记录没写成"的半拉子状态。
func InsertTx(tx *gorm.DB, c Change) error {
	row := changeRow{
		ID:        c.ID,
		ServerID:  c.ServerID,
		ServerKey: c.ServerKey,
		ToolName:  c.ToolName,
		Action:    c.Action,
		Detail:    c.Detail,
		Actor:     c.Actor,
		TS:        c.TS.UnixMilli(),
	}
	if row.ID == "" {
		row.ID = storeutil.NewID()
	}
	if row.TS == 0 {
		row.TS = time.Now().UnixMilli()
	}
	if err := tx.Create(&row).Error; err != nil {
		return fmt.Errorf("写入工具变更记录失败: %w", err)
	}
	return nil
}

// Insert 追加一条记录（非事务路径，供人工启停使用）。
func (s *Store) Insert(ctx context.Context, c Change) error {
	return InsertTx(s.db.WithContext(ctx), c)
}

// Query 是变更记录的查询条件。
type Query struct {
	ServerID string
	Limit    int
	Offset   int
}

// List 按时间倒序返回变更记录与总数。
func (s *Store) List(ctx context.Context, q Query) ([]Change, int, error) {
	if q.Limit <= 0 {
		q.Limit = 50
	}
	tx := s.db.WithContext(ctx).Model(&changeRow{})
	if q.ServerID != "" {
		tx = tx.Where("server_id = ?", q.ServerID)
	}

	var total int64
	if err := tx.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("统计工具变更记录失败: %w", err)
	}

	var rows []changeRow
	if err := tx.Order("ts DESC, id DESC").
		Limit(q.Limit).Offset(q.Offset).
		Find(&rows).Error; err != nil {
		return nil, 0, fmt.Errorf("查询工具变更记录失败: %w", err)
	}

	out := make([]Change, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.toChange())
	}
	return out, int(total), nil
}
