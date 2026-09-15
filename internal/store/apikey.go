package store

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

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrAPIKeyNameRequired 表示创建接入 key 时没给名字。
var ErrAPIKeyNameRequired = errors.New("api key 需要一个 name 来标识调用方")

// KeyPrefix 是接入 key 的明文前缀，让密钥在用户的配置文件里可辨认。
const KeyPrefix = "mh-"

// APIKey 是 api_keys 表的一行（不含任何能用来鉴权的秘密）。
type APIKey struct {
	ID      string
	Name    string
	Prefix  string // 明文前 8 位，列表展示用
	Enabled bool

	LastUsedAt time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// CreateAPIKey 生成一把新 key，返回落库记录与**完整明文**。
//
// 明文只在这里出现这一次：库里只有 sha256，创建后谁也找不回来，
// 丢了只能删掉重建。调用方（管理 API）负责在响应里交付明文。
func (s *Store) CreateAPIKey(ctx context.Context, name string) (APIKey, string, error) {
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
		ID:        NewID(),
		Name:      name,
		Prefix:    plaintext[:10],
		KeyHash:   hashAPIKey(plaintext),
		Enabled:   1,
		CreatedMS: now,
		UpdatedMS: now,
	}
	if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
		return APIKey{}, "", fmt.Errorf("写入 api key 失败: %w", err)
	}
	return row.toAPIKey(), plaintext, nil
}

// ListAPIKeys 返回全部接入 key，按创建时间倒序。
func (s *Store) ListAPIKeys(ctx context.Context) ([]APIKey, error) {
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

// DeleteAPIKey 按 id 删除接入 key。不存在返回 ErrNotFound。
func (s *Store) DeleteAPIKey(ctx context.Context, id string) error {
	res := s.db.WithContext(ctx).Delete(&apiKeyRow{}, "id = ?", id)
	if res.Error != nil {
		return fmt.Errorf("删除 api key 失败: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// SetAPIKeyEnabled 启用/停用一把接入 key。
func (s *Store) SetAPIKeyEnabled(ctx context.Context, id string, enabled bool) error {
	res := s.db.WithContext(ctx).Model(&apiKeyRow{}).
		Where("id = ?", id).
		Updates(map[string]any{"enabled": boolToInt(enabled), "updated_at": time.Now().UnixMilli()})
	if res.Error != nil {
		return fmt.Errorf("更新 api key 状态失败: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// CountAPIKeys 返回接入 key 总数与启用数。
//
// "总数是否为 0"决定 /mcp 端点是否要求鉴权：一把都没建过时开放，
// 建过就强制——按"存在"而非"启用"判断，全部停用意味着锁死端点
// 而不是放开匿名访问（见 httpd 的 mcpAuth）。
func (s *Store) CountAPIKeys(ctx context.Context) (total, enabled int, err error) {
	var rows []apiKeyRow
	if err := s.db.WithContext(ctx).
		Select("enabled").
		Find(&rows).Error; err != nil {
		return 0, 0, fmt.Errorf("统计 api key 失败: %w", err)
	}
	for _, r := range rows {
		total++
		if intToBool(r.Enabled) {
			enabled++
		}
	}
	return total, enabled, nil
}

// LookupAPIKey 按明文查找对应的启用中的 key。
//
// 常量时间比较明文哈希没有意义（sha256 输出就是比较对象），
// 这里直接按 hash 索引查；常量时间的必要性在"防止逐字节猜出
// 库里的 hash"，而 hash 本身就是 256 位随机值，猜不动。
func (s *Store) LookupAPIKey(ctx context.Context, plaintext string) (APIKey, bool, error) {
	if plaintext == "" {
		return APIKey{}, false, nil
	}
	var row apiKeyRow
	err := s.db.WithContext(ctx).
		Where("key_hash = ?", hashAPIKey(plaintext)).
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

// TouchAPIKey 更新 last_used_at。
//
// 校验路径上的写必须可忽略：用量统计晚一分钟无关紧要，
// 绝不能因为这次 UPDATE 失败让一次合法调用变成 500。
func (s *Store) TouchAPIKey(ctx context.Context, id string, at time.Time) {
	_ = s.db.WithContext(ctx).Model(&apiKeyRow{}).
		Where("id = ?", id).
		Update("last_used_at", at.UnixMilli()).Error
}

// VerifyAPIKeyPlaintext 供测试与特殊路径使用：常量时间比较两个明文。
func VerifyAPIKeyPlaintext(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func hashAPIKey(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}
