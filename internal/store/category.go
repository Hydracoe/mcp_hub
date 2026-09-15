package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

// Category 是 categories 表的一行。
//
// 单值：一个 server 至多属于一个分类（servers.category_id）。
// 多标签需求由 servers.tags 承担，因此这里没有关联表——
// 引入多对多会让"自动归类"退化成"自动打一堆标签"，
// 而用户拍板的是"一个 server 一个分类，可调整"。
type Category struct {
	ID          string
	Name        string
	Description string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	// ServerCount 是该分类下的 server 数，仅 ListCategories 填充。
	ServerCount int
}

// CreateCategory 新建分类。名字撞车时返回 ErrCategoryTaken。
func (s *Store) CreateCategory(ctx context.Context, name, description string) (Category, error) {
	name = trimName(name)
	if name == "" {
		return Category{}, fmt.Errorf("分类名不能为空")
	}
	if err := s.checkCategoryNameFree(ctx, name, ""); err != nil {
		return Category{}, err
	}

	now := time.Now().UnixMilli()
	row := categoryRow{
		ID:          NewID(),
		Name:        name,
		Description: description,
		CreatedMS:   now,
		UpdatedMS:   now,
	}
	if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
		if isUniqueViolation(err) {
			return Category{}, fmt.Errorf("%w: %q", ErrCategoryTaken, name)
		}
		return Category{}, fmt.Errorf("创建分类失败: %w", err)
	}
	return row.toCategory(), nil
}

// UpdateCategory 更新分类名或描述。
func (s *Store) UpdateCategory(ctx context.Context, id, name, description string) (Category, error) {
	name = trimName(name)
	if name == "" {
		return Category{}, fmt.Errorf("分类名不能为空")
	}
	if err := s.checkCategoryNameFree(ctx, name, id); err != nil {
		return Category{}, err
	}

	res := s.db.WithContext(ctx).Model(&categoryRow{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"name":        name,
			"description": description,
			"updated_at":  time.Now().UnixMilli(),
		})
	if err := requireAffected(res.RowsAffected, res.Error, "更新分类"); err != nil {
		return Category{}, err
	}
	return s.GetCategory(ctx, id)
}

// GetCategory 按 id 查询。
func (s *Store) GetCategory(ctx context.Context, id string) (Category, error) {
	var row categoryRow
	err := s.db.WithContext(ctx).Where("id = ?", id).Take(&row).Error
	switch {
	case err == nil:
		return row.toCategory(), nil
	case errors.Is(err, gorm.ErrRecordNotFound):
		return Category{}, ErrNotFound
	default:
		return Category{}, fmt.Errorf("解析分类行失败: %w", err)
	}
}

// ListCategories 返回全部分类（含 server 计数），按名字排序。
func (s *Store) ListCategories(ctx context.Context) ([]Category, error) {
	var rows []categoryRow
	err := s.db.WithContext(ctx).Order("name").Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("查询分类列表失败: %w", err)
	}

	counts, err := s.categoryServerCounts(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]Category, 0, len(rows))
	for _, r := range rows {
		c := r.toCategory()
		c.ServerCount = counts[c.ID]
		out = append(out, c)
	}
	return out, nil
}

// DeleteCategory 删除分类，并把引用它的 server 摘出来（category_id 置空）。
//
// category_id 没有外键约束（与 call_logs.server_id 同策略），
// 级联清理只能显式做，否则删除分类会留下悬空引用。
func (s *Store) DeleteCategory(ctx context.Context, id string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&serverRow{}).
			Where("category_id = ?", id).
			Update("category_id", "").Error; err != nil {
			return fmt.Errorf("清空 server 分类引用失败: %w", err)
		}
		res := tx.Where("id = ?", id).Delete(&categoryRow{})
		return requireAffected(res.RowsAffected, res.Error, "删除分类")
	})
}

// checkCategoryNameFree 预检分类名是否被别的分类占用。
func (s *Store) checkCategoryNameFree(ctx context.Context, name, selfID string) error {
	var holders []string
	err := s.db.WithContext(ctx).Model(&categoryRow{}).
		Where("name = ?", name).Limit(1).Pluck("id", &holders).Error
	if err != nil {
		return fmt.Errorf("检查分类名冲突失败: %w", err)
	}
	if len(holders) > 0 && holders[0] != selfID {
		return fmt.Errorf("%w: %q", ErrCategoryTaken, name)
	}
	return nil
}

// categoryServerCounts 返回分类 id → server 数。
func (s *Store) categoryServerCounts(ctx context.Context) (map[string]int, error) {
	type row struct {
		CategoryID string
		Count      int64
	}
	var rows []row
	err := s.db.WithContext(ctx).Model(&serverRow{}).
		Select("category_id, COUNT(*) AS count").
		Where("category_id <> ''").
		Group("category_id").
		Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("统计分类下的 server 数失败: %w", err)
	}
	out := make(map[string]int, len(rows))
	for _, r := range rows {
		out[r.CategoryID] = int(r.Count)
	}
	return out, nil
}

func trimName(s string) string { return strings.TrimSpace(s) }
