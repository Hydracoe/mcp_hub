package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hydra/mcp-hub/internal/downstream"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Server 是 servers 表的一行。
type Server struct {
	Spec downstream.Spec

	Enabled bool
	Status  string // healthy | degraded | offline | unknown

	// 元数据（PRD §4.7）。Description/Tags/CategoryID 人工维护；
	// Instructions 由探活自动落库，配置写入不覆盖它。
	Description  string
	Tags         []string
	Instructions string
	CategoryID   string

	// 工具过滤：控制该下游的哪些工具进入聚合池。
	ToolFilterMode ToolFilterMode
	ToolFilterList []string

	// EstTokensPerCall 是 TPM 的中间回退：网关发请求前拿不到 token 数，
	// 用这个值预估。0 表示不计量——显示成"0 / 100k"会让人以为一切正常。
	EstTokensPerCall int

	ProtocolEra   string // modern | legacy
	ServerName    string
	ServerVersion string
	Capabilities  []string

	LastCheckAt   time.Time
	LastLatencyMS int64
	LastErrorKind downstream.ErrorKind
	LastError     string

	CreatedAt time.Time
	UpdatedAt time.Time
}

// Config 是一次 server 配置写入的完整内容。
//
// 用结构体而不是多个位置参数：调用点多起来之后，
// UpsertServer(ctx, spec, true, mode, list, ...) 这种签名没人读得懂。
type Config struct {
	Spec           downstream.Spec
	Enabled        bool
	ToolFilterMode ToolFilterMode
	ToolFilterList []string
	// EstTokensPerCall 同 Server.EstTokensPerCall。负数按 0 处理。
	EstTokensPerCall int

	// 元数据。Instructions 不在这一次写入的范围内——
	// 它是探活的产物，配置保存不该把它抹掉。
	Description string
	Tags        []string
	CategoryID  string
}

// UpsertServer 按 id 插入或更新一个 server 配置。
func (s *Store) UpsertServer(ctx context.Context, cfg Config) error {
	spec := cfg.Spec
	if err := spec.Validate(); err != nil {
		return err
	}
	mode := cfg.ToolFilterMode.Normalize()
	list := CleanToolNames(cfg.ToolFilterList)
	if err := mode.Validate(list); err != nil {
		return err
	}
	tags := cleanTags(cfg.Tags)

	if spec.ID == "" {
		existing, err := s.GetServerByKey(ctx, spec.Key)
		switch {
		case err == nil:
			spec.ID = existing.Spec.ID
		case errors.Is(err, ErrNotFound):
			spec.ID = NewID()
		default:
			return err
		}
	}

	// key 有唯一约束。撞车时给出可读的错误而不是把数据库的约束报错
	// 原样抛给用户——后者只会显示一串 SQL 上下文。
	if err := s.checkKeyFree(ctx, spec.Key, spec.ID); err != nil {
		return err
	}

	now := time.Now().UnixMilli()
	estTokens := cfg.EstTokensPerCall
	if estTokens < 0 {
		estTokens = 0
	}

	row := serverRow{
		ID:               spec.ID,
		Key:              spec.Key,
		Name:             spec.Name,
		Transport:        string(spec.Transport),
		URL:              spec.URL,
		Headers:          jsonOrEmptyMap(spec.Headers),
		AuthMode:         spec.AuthMode,
		Command:          spec.Command,
		Args:             jsonOrEmptySlice(spec.Args),
		Env:              jsonOrEmptySlice(spec.Env),
		TimeoutMS:        spec.TimeoutOr().Milliseconds(),
		ProtocolVersion:  spec.ProtocolVersion,
		LegacyOnly:       boolToInt(spec.LegacyOnly),
		ToolFilterMode:   string(mode),
		ToolFilterList:   jsonOrEmptySlice(list),
		EstTokensPerCall: int64(estTokens),
		Enabled:          boolToInt(cfg.Enabled),
		Description:      cfg.Description,
		Tags:             jsonOrEmptySlice(tags),
		CategoryID:       cfg.CategoryID,
		CreatedMS:        now,
		UpdatedMS:        now,
		// 探活结果与状态不在这一次写入的范围内：配置保存不该把
		// "上次看到的健康状态"抹成默认值。instructions 同理。
		Status: "unknown",
	}

	err := s.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "id"}},
			// created_at 刻意不更新：它是"这行什么时候进来的"，
			// 而不是"最后一次改动"。
			DoUpdates: clause.AssignmentColumns([]string{
				"key", "name", "transport", "url", "headers", "auth_mode", "command", "args", "env",
				"timeout_ms", "protocol_version", "legacy_only",
				"tool_filter_mode", "tool_filter_list", "est_tokens_per_call",
				"enabled", "description", "tags", "category_id", "updated_at",
			}),
		}).
		Create(&row).Error
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("%w: %q", ErrKeyTaken, spec.Key)
		}
		return fmt.Errorf("写入 server 失败: %w", err)
	}
	return nil
}

// cleanTags 去掉空白项并保持顺序。
func cleanTags(v []string) []string {
	out := make([]string, 0, len(v))
	for _, t := range v {
		if trimmed := strings.TrimSpace(t); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// checkKeyFree 预检 key 是否被别的 server 占用。
//
// 先查一次再写，是为了给出可读错误。真正的约束由唯一索引兜底，
// 并发写入仍可能撞车，因此 Create 的 unique violation 也要捕获。
func (s *Store) checkKeyFree(ctx context.Context, key, selfID string) error {
	var holders []string
	err := s.db.WithContext(ctx).Model(&serverRow{}).
		Where("key = ?", key).Limit(1).Pluck("id", &holders).Error
	if err != nil {
		return fmt.Errorf("检查 key 冲突失败: %w", err)
	}
	if len(holders) > 0 && holders[0] != selfID {
		return fmt.Errorf("%w: %q", ErrKeyTaken, key)
	}
	return nil
}

// GetServer 按 id 查询。
func (s *Store) GetServer(ctx context.Context, id string) (Server, error) {
	var row serverRow
	err := s.db.WithContext(ctx).Where("id = ?", id).Take(&row).Error
	return scanServer(row, err)
}

// GetServerByKey 按工具前缀查询。
func (s *Store) GetServerByKey(ctx context.Context, key string) (Server, error) {
	var row serverRow
	err := s.db.WithContext(ctx).Where("key = ?", key).Take(&row).Error
	return scanServer(row, err)
}

// ListServers 返回全部 server，按 key 排序。
func (s *Store) ListServers(ctx context.Context) ([]Server, error) {
	var rows []serverRow
	err := s.db.WithContext(ctx).
		Order(clause.OrderByColumn{Column: clause.Column{Name: "key"}}).
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("查询 server 列表失败: %w", err)
	}

	out := make([]Server, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.toServer())
	}
	return out, nil
}

// SetEnabled 启用或禁用一个 server。
func (s *Store) SetEnabled(ctx context.Context, id string, enabled bool) error {
	res := s.db.WithContext(ctx).Model(&serverRow{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"enabled":    boolToInt(enabled),
			"updated_at": time.Now().UnixMilli(),
		})
	return requireAffected(res.RowsAffected, res.Error, "更新启用状态")
}

// SetServerCategory 设置 server 的单值分类（空串表示清除）。
func (s *Store) SetServerCategory(ctx context.Context, id, categoryID string) error {
	res := s.db.WithContext(ctx).Model(&serverRow{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"category_id": categoryID,
			"updated_at":  time.Now().UnixMilli(),
		})
	return requireAffected(res.RowsAffected, res.Error, "更新分类")
}

// DeleteServer 删除 server，工具缓存与密钥随外键级联删除。
func (s *Store) DeleteServer(ctx context.Context, id string) error {
	res := s.db.WithContext(ctx).Where("id = ?", id).Delete(&serverRow{})
	return requireAffected(res.RowsAffected, res.Error, "删除 server")
}

// SaveProbe 把探活结果写回 servers，并在成功时整体替换该 server 的工具缓存。
//
// 探活失败只更新状态与错误信息，不动已有工具缓存——
// 下游抖动不应让已经聚合好的工具凭空消失。
func (s *Store) SaveProbe(ctx context.Context, serverID string, res *downstream.ProbeResult) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 顺带拿到 key：工具缓存里的 exposed_name 是 <key>__<tool>，
		// 而 key 只存在 servers 表里。
		var keys []string
		if err := tx.Model(&serverRow{}).
			Where("id = ?", serverID).Limit(1).Pluck("key", &keys).Error; err != nil {
			return fmt.Errorf("查询 server 失败: %w", err)
		}
		if len(keys) == 0 {
			return ErrNotFound
		}

		status, era := "offline", ""
		if res.OK {
			status = "healthy"
			era = "modern"
			if res.Legacy {
				era = "legacy"
			}
		}

		updates := map[string]any{
			"status":          status,
			"protocol_era":    era,
			"server_name":     res.ServerName,
			"server_version":  res.ServerVersion,
			"capabilities":    jsonOrEmptySlice(res.Capabilities),
			"last_check_at":   res.CheckedAt.UnixMilli(),
			"last_latency_ms": res.Latency.Total,
			"last_error_kind": string(res.Kind),
			"last_error":      res.Detail,
			"updated_at":      time.Now().UnixMilli(),
		}
		// 探活报回来的协议版本只在非空时覆盖：空表示"这次没协商出来"，
		// 把已知的版本抹掉会让下次连接回退到自动协商。
		if res.ProtocolVersion != "" {
			updates["protocol_version"] = res.ProtocolVersion
		}
		// instructions 同理：非空才落库（PRD §4.7.2）。
		if res.Instructions != "" {
			updates["instructions"] = res.Instructions
		}

		if err := tx.Model(&serverRow{}).Where("id = ?", serverID).
			Updates(updates).Error; err != nil {
			return fmt.Errorf("更新探活结果失败: %w", err)
		}

		if res.OK {
			if err := replaceTools(tx, serverID, keys[0], res.Tools); err != nil {
				return err
			}
		}
		return nil
	})
}

// replaceTools 整体替换某个 server 的工具缓存。
//
// "整体替换"而不是增量 upsert：下游改了工具签名时，增量更新会把
// 已经删掉的工具永远留在列表里，而 AI 会一直尝试调用它们。
func replaceTools(tx *gorm.DB, serverID, serverKey string, tools []downstream.ToolInfo) error {
	if err := tx.Where("server_id = ?", serverID).Delete(&toolRow{}).Error; err != nil {
		return fmt.Errorf("清理旧工具缓存失败: %w", err)
	}
	if len(tools) == 0 {
		return nil
	}

	now := time.Now().UnixMilli()
	rows := make([]toolRow, 0, len(tools))
	for _, t := range tools {
		schema := string(t.InputSchema)
		if schema == "" {
			schema = "{}"
		}
		rows = append(rows, toolRow{
			ID:          NewID(),
			ServerID:    serverID,
			Name:        t.Name,
			ExposedName: serverKey + "__" + t.Name,
			Description: t.Description,
			InputSchema: schema,
			Enabled:     1,
			CachedMS:    now,
		})
	}

	if err := tx.CreateInBatches(&rows, 200).Error; err != nil {
		return fmt.Errorf("写入工具缓存失败(%d 个): %w", len(rows), err)
	}
	return nil
}

// MarkNoUsableKey 把一个 server 标为降级，原因是"没有可用的密钥"。
//
// 用 degraded 而不是 offline：服务本身是通的，只是没有一把凭据能过。
// 两者的下一步动作完全不同——offline 要去查网络，degraded 要去查
// 密钥与账号额度。归错类会让用户在一个方向上白花时间。
func (s *Store) MarkNoUsableKey(ctx context.Context, serverID, detail string) error {
	err := s.db.WithContext(ctx).Model(&serverRow{}).
		Where("id = ?", serverID).
		Updates(map[string]any{
			"status":          "degraded",
			"last_error_kind": string(downstream.KindNoUsableKey),
			"last_error":      detail,
			"updated_at":      time.Now().UnixMilli(),
		}).Error
	if err != nil {
		return fmt.Errorf("更新 server 降级状态失败: %w", err)
	}
	return nil
}

func scanServer(row serverRow, err error) (Server, error) {
	switch {
	case err == nil:
		return row.toServer(), nil
	case errors.Is(err, gorm.ErrRecordNotFound):
		return Server{}, ErrNotFound
	default:
		return Server{}, fmt.Errorf("解析 server 行失败: %w", err)
	}
}
