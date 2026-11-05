// Package categories 是 categories 表的持久化：server 的单值分类。
package categories

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/hydra/mcp-hub/internal/store/storeutil"

	"gorm.io/gorm"
)

// categoryRow 是 categories 表的一行。
// Name 是中文展示名，Slug 是英文名（进对外工具名前缀段）。
type categoryRow struct {
	ID          string `gorm:"column:id;primaryKey"`
	Name        string `gorm:"column:name"`
	Slug        string `gorm:"column:slug"`
	Description string `gorm:"column:description"`
	CreatedMS   int64  `gorm:"column:created_at"`
	UpdatedMS   int64  `gorm:"column:updated_at"`
}

func (categoryRow) TableName() string { return "categories" }

func (r categoryRow) toCategory() Category {
	return Category{
		ID:          r.ID,
		Name:        r.Name,
		Slug:        r.Slug,
		Description: r.Description,
		CreatedAt:   time.UnixMilli(r.CreatedMS),
		UpdatedAt:   time.UnixMilli(r.UpdatedMS),
	}
}

// Category 是 categories 表的一行（领域形态）。单值：一个 server 至多属于一个分类
// （servers.category_id），多标签由 servers.tags 承担。Name 与 Slug 都必填且各自全局唯一：
// Name 是中文展示名，Slug 限定 [a-z0-9-] 给机器用；两者之间不做任何转换——填错就报错，不替用户猜。
type Category struct {
	ID          string
	Name        string
	Slug        string
	Description string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	ServerCount int
	ToolCount   int
}

// maxCategorySlugLen 限制英文名长度：slug 出现在列表与筛选参数里，太长扫不动。
const maxCategorySlugLen = 32

// categorySlugPattern 约束分类英文名的形态：小写字母数字，单词之间单个连字符。
// 字符集与 servers.key 同源（都进标识符）；全小写要求保证不能只用大小写区分两个分类。
var categorySlugPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// checkCategorySlug 校验英文名，合法则原样返回。刻意不做任何改写：不合规就报错。
// 静默改写会出现"保存成功但存下来的不是用户填的值"，且不同输入会撞成同一 slug；
// 只去掉首尾空白——那是看不见的输入噪声，不是内容。
func checkCategorySlug(raw string) (string, error) {
	slug := strings.TrimSpace(raw)
	switch {
	case slug == "":
		return "", fmt.Errorf("%w: 英文名不能为空", ErrCategorySlugInvalid)
	case len(slug) > maxCategorySlugLen:
		return "", fmt.Errorf("%w: 英文名不能超过 %d 个字符（当前 %d）",
			ErrCategorySlugInvalid, maxCategorySlugLen, len(slug))
	case !categorySlugPattern.MatchString(slug):
		return "", fmt.Errorf("%w: 英文名 %q 只能用小写字母、数字和连字符，"+
			"连字符只能出现在中间，且不能连续", ErrCategorySlugInvalid, slug)
	}
	return slug, nil
}

// Store 是 categories 表的读写入口。
type Store struct {
	db *gorm.DB
}

// New 构造 categories 包的读写入口。
func New(db *gorm.DB) *Store { return &Store{db: db} }

// Create 新建分类。中文名与英文名都是必填且各自唯一：
// 空名 → ErrCategoryNameRequired / ErrCategorySlugInvalid，
// 撞车 → ErrCategoryTaken / ErrCategorySlugTaken。
func (s *Store) Create(ctx context.Context, name, slug, description string) (Category, error) {
	name = trimName(name)
	if name == "" {
		return Category{}, ErrCategoryNameRequired
	}
	slug, err := checkCategorySlug(slug)
	if err != nil {
		return Category{}, err
	}
	if err := s.checkNameFree(ctx, name, ""); err != nil {
		return Category{}, err
	}
	if err := s.checkSlugFree(ctx, slug, ""); err != nil {
		return Category{}, err
	}

	now := time.Now().UnixMilli()
	row := categoryRow{
		ID:          storeutil.NewID(),
		Name:        name,
		Slug:        slug,
		Description: description,
		CreatedMS:   now,
		UpdatedMS:   now,
	}
	if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
		if storeutil.IsUniqueViolation(err) {
			return Category{}, fmt.Errorf("%w: %q", ErrCategoryTaken, name)
		}
		return Category{}, fmt.Errorf("创建分类失败: %w", err)
	}
	return row.toCategory(), nil
}

// Update 更新分类的中文名、英文名或描述。两个名字同样必填、同样判重，
// 改名失败时整行不动（预检都在 UPDATE 之前）。
func (s *Store) Update(ctx context.Context, id, name, slug, description string) (Category, error) {
	name = trimName(name)
	if name == "" {
		return Category{}, ErrCategoryNameRequired
	}
	slug, err := checkCategorySlug(slug)
	if err != nil {
		return Category{}, err
	}
	if err := s.checkNameFree(ctx, name, id); err != nil {
		return Category{}, err
	}
	if err := s.checkSlugFree(ctx, slug, id); err != nil {
		return Category{}, err
	}

	res := s.db.WithContext(ctx).Model(&categoryRow{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"name":        name,
			"slug":        slug,
			"description": description,
			"updated_at":  time.Now().UnixMilli(),
		})
	if res.Error != nil {
		// 预检与 UPDATE 之间有并发窗口：别的请求可能刚占用了这个名字。
		// 重查后把撞车翻译成与预检同样的领域错误（上层映射 409），不让它变成 500。
		if storeutil.IsUniqueViolation(res.Error) {
			if err := s.checkNameFree(ctx, name, id); err != nil {
				return Category{}, err
			}
			if err := s.checkSlugFree(ctx, slug, id); err != nil {
				return Category{}, err
			}
			// 占用方此时又放开了：仍按撞车报，让用户重试一次即可。
			return Category{}, fmt.Errorf("%w: %q", ErrCategoryTaken, name)
		}
		return Category{}, fmt.Errorf("更新分类失败: %w", res.Error)
	}
	if err := storeutil.RequireAffected(res.RowsAffected, nil, "更新分类"); err != nil {
		return Category{}, err
	}
	return s.Get(ctx, id)
}

// Get 按 id 查询。
func (s *Store) Get(ctx context.Context, id string) (Category, error) {
	var row categoryRow
	err := s.db.WithContext(ctx).Where("id = ?", id).Take(&row).Error
	switch {
	case err == nil:
		return row.toCategory(), nil
	case errors.Is(err, gorm.ErrRecordNotFound):
		return Category{}, storeutil.ErrNotFound
	default:
		return Category{}, fmt.Errorf("解析分类行失败: %w", err)
	}
}

// List 返回全部分类（含 server 与工具计数），按名字排序。
func (s *Store) List(ctx context.Context) ([]Category, error) {
	var rows []categoryRow
	err := s.db.WithContext(ctx).Order("name").Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("查询分类列表失败: %w", err)
	}

	counts, err := s.serverCounts(ctx)
	if err != nil {
		return nil, err
	}
	toolCounts, err := s.toolCounts(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]Category, 0, len(rows))
	for _, r := range rows {
		c := r.toCategory()
		c.ServerCount = counts[c.ID]
		c.ToolCount = toolCounts[c.ID]
		out = append(out, c)
	}
	return out, nil
}

// Delete 删除分类。分类下还挂着 server 时拒绝删除（ErrCategoryInUse），
// 不做静默的批量改归类的副作用。判空与删除写在同一条语句里（NOT EXISTS 子查询），
// 避免先查后删的窗口；category_id 没有外键（空串是合法值），这层约束只能落在应用里。
func (s *Store) Delete(ctx context.Context, id string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Where("id = ?", id).
			Where("NOT EXISTS (SELECT 1 FROM servers WHERE category_id = ?)", id).
			Delete(&categoryRow{})
		if res.Error != nil {
			return fmt.Errorf("删除分类失败: %w", res.Error)
		}
		if res.RowsAffected > 0 {
			return nil
		}
		// 一行都没删掉只有两种可能：分类不存在，或仍被 server 引用。
		// 分开报是为了让上层能给出 404 与 409 两种不同的处置建议。
		var refs int64
		if err := tx.Table("servers").Where("category_id = ?", id).Count(&refs).Error; err != nil {
			return fmt.Errorf("统计分类下的 server 数失败: %w", err)
		}
		if refs > 0 {
			return fmt.Errorf("%w: 仍有 %d 个 server 属于该分类", ErrCategoryInUse, refs)
		}
		return storeutil.ErrNotFound
	})
}

// checkNameFree 预检分类中文名是否被占用。
func (s *Store) checkNameFree(ctx context.Context, name, selfID string) error {
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

// checkSlugFree 预检分类英文名是否被占用。空 slug 兜底放行：
// 库里存量行可能留着空 slug，它们之间不算冲突。
func (s *Store) checkSlugFree(ctx context.Context, slug, selfID string) error {
	if slug == "" {
		return nil
	}
	var holders []string
	err := s.db.WithContext(ctx).Model(&categoryRow{}).
		Where("slug = ?", slug).Limit(1).Pluck("id", &holders).Error
	if err != nil {
		return fmt.Errorf("检查分类英文名冲突失败: %w", err)
	}
	if len(holders) > 0 && holders[0] != selfID {
		return fmt.Errorf("%w: %q", ErrCategorySlugTaken, slug)
	}
	return nil
}

// serverCounts 返回分类 id → server 数。
func (s *Store) serverCounts(ctx context.Context) (map[string]int, error) {
	type row struct {
		CategoryID string
		Count      int64
	}
	var rows []row
	err := s.db.WithContext(ctx).
		Table("servers").
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

// toolCounts 返回分类 id → 工具数。取工具缓存的全量（含被过滤名单挡住的）：
// "分类装了多少"与聚合层"此刻暴露几个"是两个口径，混用会让改过滤名单像删了工具。
func (s *Store) toolCounts(ctx context.Context) (map[string]int, error) {
	type row struct {
		CategoryID string
		Count      int64
	}
	var rows []row
	err := s.db.WithContext(ctx).
		Table("servers AS s").
		Select("s.category_id AS category_id, COUNT(t.id) AS count").
		Joins("JOIN tools AS t ON t.server_id = s.id").
		Where("s.category_id <> ''").
		Group("s.category_id").
		Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("统计分类下的工具数失败: %w", err)
	}
	out := make(map[string]int, len(rows))
	for _, r := range rows {
		out[r.CategoryID] = int(r.Count)
	}
	return out, nil
}

func trimName(s string) string { return strings.TrimSpace(s) }
