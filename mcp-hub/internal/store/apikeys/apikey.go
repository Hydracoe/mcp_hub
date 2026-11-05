// Package apikeys 是 api_keys 表的持久化：外部 AI 客户端调 hub 的接入 key。
package apikeys

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hydra/mcp-hub/internal/store/storeutil"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrAPIKeyNameRequired = errors.New("api key 需要一个 name 来标识调用方")

const KeyPrefix = "mh-"

type apiKeyRow struct {
	ID         string `gorm:"column:id;primaryKey"`
	Name       string `gorm:"column:name"`
	Prefix     string `gorm:"column:prefix"`
	KeyHash    string `gorm:"column:key_hash"`
	Enabled    int64  `gorm:"column:enabled"`
	LastUsedMS int64  `gorm:"column:last_used_at"`
	CreatedMS  int64  `gorm:"column:created_at"`
	UpdatedMS  int64  `gorm:"column:updated_at"`
}

func (apiKeyRow) TableName() string { return "api_keys" }

func (r apiKeyRow) toAPIKey() APIKey {
	return APIKey{
		ID:         r.ID,
		Name:       r.Name,
		Prefix:     r.Prefix,
		Enabled:    storeutil.IntToBool(r.Enabled),
		LastUsedAt: time.UnixMilli(r.LastUsedMS),
		CreatedAt:  time.UnixMilli(r.CreatedMS),
		UpdatedAt:  time.UnixMilli(r.UpdatedMS),
	}
}

// APIKey 是 api_keys 表的一行（领域形态，不含任何能用来鉴权的秘密）。
type APIKey struct {
	ID      string
	Name    string
	Prefix  string // 明文前 8 位，列表展示用
	Enabled bool

	LastUsedAt time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// Store 是 api_keys 表的读写入口。
type Store struct {
	db *gorm.DB
}

// New 构造 apikeys 包的读写入口。
func New(db *gorm.DB) *Store { return &Store{db: db} }

// Create 生成一把新 key，返回落库记录与完整明文。
// 明文只在这里出现这一次：库里只有 sha256，丢了只能删掉重建。
func (s *Store) Create(ctx context.Context, name string) (APIKey, string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return APIKey{}, "", ErrAPIKeyNameRequired
	}

	secret := make([]byte, 16)
	if _, err := rand.Read(secret); err != nil {
		return APIKey{}, "", fmt.Errorf("生成密钥失败: %w", err)
	}
	plaintext := KeyPrefix + hex.EncodeToString(secret)

	now := time.Now().UnixMilli()
	row := apiKeyRow{
		ID:        storeutil.NewID(),
		Name:      name,
		Prefix:    plaintext[:10],
		KeyHash:   hash(plaintext),
		Enabled:   1,
		CreatedMS: now,
		UpdatedMS: now,
	}
	if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
		return APIKey{}, "", fmt.Errorf("写入 api key 失败: %w", err)
	}
	return row.toAPIKey(), plaintext, nil
}

// List 返回全部接入 key，按创建时间倒序。
func (s *Store) List(ctx context.Context) ([]APIKey, error) {
	var rows []apiKeyRow
	err := s.db.WithContext(ctx).
		Order(clause.OrderByColumn{Column: clause.Column{Name: "created_at"}, Desc: true}).
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("查询 api key 失败: %w", err)
	}
	out := make([]APIKey, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.toAPIKey())
	}
	return out, nil
}

// Delete 按 id 删除接入 key。不存在返回 storeutil.ErrNotFound。
func (s *Store) Delete(ctx context.Context, id string) error {
	res := s.db.WithContext(ctx).Delete(&apiKeyRow{}, "id = ?", id)
	if res.Error != nil {
		return fmt.Errorf("删除 api key 失败: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return storeutil.ErrNotFound
	}
	return nil
}

// SetEnabled 启用/停用一把接入 key。
func (s *Store) SetEnabled(ctx context.Context, id string, enabled bool) error {
	res := s.db.WithContext(ctx).Model(&apiKeyRow{}).
		Where("id = ?", id).
		Updates(map[string]any{"enabled": storeutil.BoolToInt(enabled), "updated_at": time.Now().UnixMilli()})
	if res.Error != nil {
		return fmt.Errorf("更新 api key 状态失败: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return storeutil.ErrNotFound
	}
	return nil
}

// Count 返回接入 key 总数与启用数。
// "总数是否为 0"决定 /mcp 端点是否要求鉴权：一把都没建过时开放，
// 建过就强制——全部停用意味着锁死端点，而不是放开匿名访问。
func (s *Store) Count(ctx context.Context) (total, enabled int, err error) {
	var rows []apiKeyRow
	if err := s.db.WithContext(ctx).
		Select("enabled").
		Find(&rows).Error; err != nil {
		return 0, 0, fmt.Errorf("统计 api key 失败: %w", err)
	}
	for _, r := range rows {
		total++
		if storeutil.IntToBool(r.Enabled) {
			enabled++
		}
	}
	return total, enabled, nil
}

// Lookup 按明文查找对应的启用中的 key（直接按 hash 索引查）。
func (s *Store) Lookup(ctx context.Context, plaintext string) (APIKey, bool, error) {
	if plaintext == "" {
		return APIKey{}, false, nil
	}
	var row apiKeyRow
	err := s.db.WithContext(ctx).
		Where("key_hash = ?", hash(plaintext)).
		Take(&row).Error
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		return APIKey{}, false, nil
	case err != nil:
		return APIKey{}, false, fmt.Errorf("查询 api key 失败: %w", err)
	}
	key := row.toAPIKey()
	if !key.Enabled {
		return key, false, nil
	}
	return key, true, nil
}

// Touch 更新 last_used_at。校验路径上的写必须可忽略：
// 绝不能因为这次 UPDATE 失败让一次合法调用变成 500。
func (s *Store) Touch(ctx context.Context, id string, at time.Time) {
	_ = s.db.WithContext(ctx).Model(&apiKeyRow{}).
		Where("id = ?", id).
		Update("last_used_at", at.UnixMilli()).Error
}

// VerifyPlaintext 供测试与特殊路径使用：常量时间比较两个明文。
func VerifyPlaintext(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func hash(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}
