package store

import (
	"context"
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// app_config 是 key-value 的运行时配置表，value 一律存 JSON 字符串。
//
// 领域结构体（如 EmbedConfig）由上层定义与编解码，store 只负责
// 存取原始字符串——避免持久层反向依赖业务包。

// GetConfig 返回 key 对应的原始 JSON 字符串；不存在时返回空串、无错误。
func (s *Store) GetConfig(ctx context.Context, key string) (string, error) {
	var row configRow
	err := s.db.WithContext(ctx).Where("key = ?", key).Take(&row).Error
	switch err {
	case nil:
		return row.Value, nil
	case gorm.ErrRecordNotFound:
		return "", nil
	default:
		return "", fmt.Errorf("读取配置失败: %w", err)
	}
}

// SetConfig 写入（不存在则创建）一条配置。
func (s *Store) SetConfig(ctx context.Context, key, value string) error {
	err := s.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "key"}},
			DoUpdates: clause.AssignmentColumns([]string{"value"}),
		}).
		Create(&configRow{Key: key, Value: value}).Error
	if err != nil {
		return fmt.Errorf("写入配置失败: %w", err)
	}
	return nil
}
