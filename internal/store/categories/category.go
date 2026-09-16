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

// Category 是 categories 表的一行（领域形态）。
//
// 单值：一个 server 至多属于一个分类（servers.category_id），多标签由
// servers.tags 承担，因此没有关联表。
// Name 与 Slug 都是必填且各自全局唯一：Name 是中文名给人看（列表、下拉、
// 检索语料）；Slug 是英文名给机器用（筛选参数、工具名前缀段），限定
// [a-z0-9-]。两者之间不做任何转换——填错一个就报错，不替用户猜。
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
//
// 字符集与 servers.key 同源（都进标识符），这里额外要求全小写——
// 英文名是分类的稳定标识，不允许只用大小写区分两个分类。
var categorySlugPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// checkCategorySlug 校验英文名，合法则原样返回。
//
// 刻意**不做任何改写**：不合规就报错，让用户看见自己填的东西哪里不对。
// 旧实现会把 "Weather" 折成 weather、"web search" 折成 web-search、
// 丢掉中文字符、静默截断到 32 字符，结果是"保存成功了，但存下来的
// 不是我填的那个值"——用户下一次照着输入框里的内容再改，反而改不动，
// 且两个不同的输入会撞成同一个 slug，冲突报错指向一个看不懂的名字。
//
// 只去掉首尾空白：那是看不见的输入噪声，不是内容。
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
	if err := storeutil.RequireAffected(res.RowsAffected, res.Error, "更新分类"); err != nil {
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

// Delete 删除分类。分类下还挂着 server 时**拒绝删除**（ErrCategoryInUse）。
//
// 早先的实现会把引用它的 server 的 category_id 置空再删分类。那是一次
// 静默的批量改动：用户点的是"删掉这个分类"，实际结果却是"这个分类下的
// 12 个 server 一起变成未分类"——多出来的改动在界面上只表现为若干行
// 突然空掉的分类列，没人会把它和刚才那次点击联系起来。要删，先显式把
// server 改归别的分类或删掉它们。
//
// 判空与删除写在**同一条语句**里（NOT EXISTS 子查询），不是先查后删：
// 两步之间有窗口，会删掉一个刚被挂上 server 的分类，留下指向不存在
// 分类的 category_id。category_id 没有外键（空串是合法值，"未分类"
// 装不进 FK），这层约束只能落在应用里。
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

// checkSlugFree 预检分类英文名是否被占用。
// 英文名已不可能为空（checkCategorySlug 拦在前面），空值分支只是兜底：
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

// toolCounts 返回分类 id → 工具数。
// 取工具缓存的全量（含被过滤名单挡住的）："分类装了多少"与
// 聚合层"此刻暴露几个"是两个口径，混用会让改过滤名单像删了工具。
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
