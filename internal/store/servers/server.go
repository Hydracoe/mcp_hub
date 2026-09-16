// Package servers 是 servers 与 tools 两张表的持久化：下游 server 的配置、
// 探活快照与工具缓存。工具缓存是 server 的从属数据（探活时整体替换、
// 随 server 级联删除），因此跟配置放同一个包，事务也不用跨包。
package servers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hydra/mcp-hub/internal/downstream"
	"github.com/hydra/mcp-hub/internal/store/storeutil"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrKeyTaken 表示工具前缀已被另一个 server 占用（上层映射 409）。
var ErrKeyTaken = errors.New("server key already in use")

// serverRow 是 servers 表的一行（持久化形态）。列映射与
// migrations/000001_init.up.sql 逐列对应，改列名时两边一起改。
type serverRow struct {
	ID              string `gorm:"column:id;primaryKey"`
	Key             string `gorm:"column:key"`
	Name            string `gorm:"column:name"`
	Transport       string `gorm:"column:transport"`
	URL             string `gorm:"column:url"`
	Headers         string `gorm:"column:headers"` // JSON object
	AuthMode        string `gorm:"column:auth_mode"`
	Command         string `gorm:"column:command"`
	Args            string `gorm:"column:args"` // JSON array
	Env             string `gorm:"column:env"`  // JSON array
	TimeoutMS       int64  `gorm:"column:timeout_ms"`
	ProtocolVersion string `gorm:"column:protocol_version"`
	LegacyOnly      int64  `gorm:"column:legacy_only"`
	// BootEnabledTools 是创建时的一次性引导名单（JSON array）：首次探活
	// 落库时，名单内的工具行直接 enabled=1，随后清空。只影响新行，
	// 不构成任何持续性过滤——工具的启停只由 tools.enabled 决定。
	BootEnabledTools string `gorm:"column:boot_enabled_tools"`
	// AutoEnable 是"上游新增工具直接放行"的 per-server 策略（O5）。
	// 默认 0：新工具须人工启用（T1 的安全默认）。开了的 server，探活
	// 新发现的工具直接 enabled=1 并留一条变更记录。
	AutoEnable int64 `gorm:"column:auto_enable"`
	Enabled    int64 `gorm:"column:enabled"`

	Description  string `gorm:"column:description"`
	Tags         string `gorm:"column:tags"`         // JSON array
	Instructions string `gorm:"column:instructions"` // 下游 serverInfo.instructions，探活自动落库
	CategoryID   string `gorm:"column:category_id"`  // 单值，无外键

	Status        string `gorm:"column:status"`
	ProtocolEra   string `gorm:"column:protocol_era"`
	ServerName    string `gorm:"column:server_name"`
	ServerVersion string `gorm:"column:server_version"`
	Capabilities  string `gorm:"column:capabilities"` // JSON array
	LastCheckAt   *int64 `gorm:"column:last_check_at"`
	LastLatencyMS *int64 `gorm:"column:last_latency_ms"`
	LastErrorKind string `gorm:"column:last_error_kind"`
	LastError     string `gorm:"column:last_error"`
	CreatedMS     int64  `gorm:"column:created_at"`
	UpdatedMS     int64  `gorm:"column:updated_at"`
}

func (serverRow) TableName() string { return "servers" }

func (r serverRow) toServer() Server {
	srv := Server{
		Spec: downstream.Spec{
			ID:              r.ID,
			Key:             r.Key,
			Name:            r.Name,
			Transport:       downstream.Transport(r.Transport),
			URL:             r.URL,
			Headers:         storeutil.DecodeStringMap(r.Headers),
			AuthMode:        r.AuthMode,
			Command:         r.Command,
			Args:            storeutil.DecodeStringSlice(r.Args),
			Env:             storeutil.DecodeStringSlice(r.Env),
			Timeout:         time.Duration(r.TimeoutMS) * time.Millisecond,
			ProtocolVersion: r.ProtocolVersion,
			LegacyOnly:      storeutil.IntToBool(r.LegacyOnly),
		},
		Enabled:       storeutil.IntToBool(r.Enabled),
		AutoEnable:    storeutil.IntToBool(r.AutoEnable),
		Status:        r.Status,
		Description:   r.Description,
		Tags:          storeutil.DecodeStringSlice(r.Tags),
		Instructions:  r.Instructions,
		CategoryID:    r.CategoryID,
		ProtocolEra:   r.ProtocolEra,
		ServerName:    r.ServerName,
		ServerVersion: r.ServerVersion,
		Capabilities:  storeutil.DecodeStringSlice(r.Capabilities),
		LastErrorKind: downstream.ErrorKind(r.LastErrorKind),
		LastError:     r.LastError,
		CreatedAt:     time.UnixMilli(r.CreatedMS),
		UpdatedAt:     time.UnixMilli(r.UpdatedMS),
	}
	if r.LastLatencyMS != nil {
		srv.LastLatencyMS = *r.LastLatencyMS
	}
	if r.LastCheckAt != nil {
		srv.LastCheckAt = time.UnixMilli(*r.LastCheckAt)
	}
	return srv
}

// Server 是 servers 表的一行（领域形态）。
type Server struct {
	Spec downstream.Spec

	Enabled bool
	// AutoEnable 是"上游新增工具直接放行"的 per-server 策略（O5）。
	// 默认 false：新工具进缓存但禁用，等人工启用（T1）。
	AutoEnable bool

	Status string // healthy | degraded | offline | unknown

	// 元数据。Description/Tags/CategoryID 人工维护；Instructions 由探活自动落库。
	Description  string
	Tags         []string
	Instructions string
	CategoryID   string

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

// Config 是一次 server 配置写入的完整内容（字段多，用结构体收拢）。
type Config struct {
	Spec    downstream.Spec
	Enabled bool

	// AutoEnable 是"上游新增工具直接放行"的开关（O5）。默认 false，
	// 即新工具须人工启用；true 只影响**此后探活新发现**的工具，
	// 不会回头去启用已经存在的（哪怕是禁用的）工具。
	AutoEnable bool

	// EnabledTools 是创建时的初始启用名单（创建向导探活后勾选的工具）。
	// 只在首次落库时生效：首次探活把名单内的工具写成 enabled=1，名单即清空。
	// 编辑配置时忽略此字段——工具的启停此后只走 tools.enabled 开关。
	EnabledTools []string

	// 元数据。Instructions 不在写入范围内——它是探活的产物，配置保存不该抹掉。
	Description string
	Tags        []string
	CategoryID  string
}

// CleanToolNames 去掉空白项并保持顺序。
func CleanToolNames(names []string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		if trimmed := strings.TrimSpace(n); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
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

// Store 是 servers/tools 表的读写入口。
type Store struct {
	db *gorm.DB
}

// New 构造 servers 包的读写入口。
func New(db *gorm.DB) *Store { return &Store{db: db} }

// Upsert 按 id 插入或更新一个 server 配置。
func (s *Store) Upsert(ctx context.Context, cfg Config) error {
	spec := cfg.Spec
	if err := spec.Validate(); err != nil {
		return err
	}
	tags := cleanTags(cfg.Tags)

	if spec.ID == "" {
		existing, err := s.GetByKey(ctx, spec.Key)
		switch {
		case err == nil:
			spec.ID = existing.Spec.ID
		case errors.Is(err, storeutil.ErrNotFound):
			spec.ID = storeutil.NewID()
		default:
			return err
		}
	}

	// key 撞车是用户输入问题，给出可读错误而不是数据库的约束报错。
	if err := s.checkKeyFree(ctx, spec.Key, spec.ID); err != nil {
		return err
	}

	now := time.Now().UnixMilli()

	row := serverRow{
		ID:               spec.ID,
		Key:              spec.Key,
		Name:             spec.Name,
		Transport:        string(spec.Transport),
		URL:              spec.URL,
		Headers:          storeutil.JSONOrEmptyMap(spec.Headers),
		AuthMode:         spec.AuthMode,
		Command:          spec.Command,
		Args:             storeutil.JSONOrEmptySlice(spec.Args),
		Env:              storeutil.JSONOrEmptySlice(spec.Env),
		TimeoutMS:        spec.TimeoutOr().Milliseconds(),
		ProtocolVersion:  spec.ProtocolVersion,
		LegacyOnly:       storeutil.BoolToInt(spec.LegacyOnly),
		BootEnabledTools: storeutil.JSONOrEmptySlice(CleanToolNames(cfg.EnabledTools)),
		AutoEnable:       storeutil.BoolToInt(cfg.AutoEnable),
		Enabled:          storeutil.BoolToInt(cfg.Enabled),
		Description:      cfg.Description,
		Tags:             storeutil.JSONOrEmptySlice(tags),
		CategoryID:       cfg.CategoryID,
		CreatedMS:        now,
		UpdatedMS:        now,
		// 探活结果与 instructions 不在写入范围内，配置保存不该把
		// "上次看到的健康状态"抹成默认值。
		Status: "unknown",
	}

	err := s.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "id"}},
			// created_at 不更新：它是"这行什么时候进来的"，不是"最后一次改动"。
			// boot_enabled_tools 也不更新：引导名单只在创建时写入，
			// 由首次成功的探活消费；编辑配置不能重建或清除它。
			DoUpdates: clause.AssignmentColumns([]string{
				"key", "name", "transport", "url", "headers", "auth_mode", "command", "args", "env",
				"timeout_ms", "protocol_version", "legacy_only",
				"enabled", "auto_enable", "description", "tags", "category_id", "updated_at",
			}),
		}).
		Create(&row).Error
	if err != nil {
		if storeutil.IsUniqueViolation(err) {
			return fmt.Errorf("%w: %q", ErrKeyTaken, spec.Key)
		}
		return fmt.Errorf("写入 server 失败: %w", err)
	}
	return nil
}

// checkKeyFree 预检 key 是否被别的 server 占用（可读错误）。
// 并发竞态由唯一索引兜底，Create 的 unique violation 也做了捕获。
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

// Get 按 id 查询。
func (s *Store) Get(ctx context.Context, id string) (Server, error) {
	var row serverRow
	err := s.db.WithContext(ctx).Where("id = ?", id).Take(&row).Error
	return scanServer(row, err)
}

// GetByKey 按工具前缀查询。
func (s *Store) GetByKey(ctx context.Context, key string) (Server, error) {
	var row serverRow
	err := s.db.WithContext(ctx).Where("key = ?", key).Take(&row).Error
	return scanServer(row, err)
}

// List 返回全部 server，按 key 排序。
func (s *Store) List(ctx context.Context) ([]Server, error) {
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

// SetEnabledCascade 启停一个 server；停用时级联禁用它的全部工具。
//
// 约束是「停用的 server 下不允许有启用的工具」：server 停用后本就从聚合
// 列表消失，但工具行还各自挂着 enabled 标记，不同步翻掉的话界面会显示
// 一堆「已启用」却对外不生效的工具。server 开关与工具级联在同一事务里，
// 不留中间态。返回本次被翻转（1→0）的工具名，供上层补变更记录。
func (s *Store) SetEnabledCascade(ctx context.Context, id string, enabled bool) ([]string, error) {
	var flipped []string
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&serverRow{}).
			Where("id = ?", id).
			Updates(map[string]any{
				"enabled":    storeutil.BoolToInt(enabled),
				"updated_at": time.Now().UnixMilli(),
			})
		if err := storeutil.RequireAffected(res.RowsAffected, res.Error, "更新启用状态"); err != nil {
			return err
		}
		if enabled {
			return nil
		}
		names, err := disableToolsTx(tx, id)
		if err != nil {
			return err
		}
		flipped = names
		return nil
	})
	if err != nil {
		return nil, err
	}
	return flipped, nil
}

// DisableToolsOfServer 把某个 server 的全部工具置为禁用（幂等）。
// 供「编辑配置把 server 关掉」的路径使用——那条路不走 SetEnabledCascade，
// server 开关已由 Upsert 写入，这里只补工具的级联。
// 返回本次真正从启用翻转为禁用的工具名，供调用方补变更记录。
func (s *Store) DisableToolsOfServer(ctx context.Context, serverID string) ([]string, error) {
	var flipped []string
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		names, err := disableToolsTx(tx, serverID)
		if err != nil {
			return err
		}
		flipped = names
		return nil
	})
	if err != nil {
		return nil, err
	}
	return flipped, nil
}

// disableToolsTx 在给定事务里把 server 的启用工具全部翻成禁用，
// 返回被翻转的工具名。「已经是禁用」的行不动也不进返回值——
// 变更记录只描述真正发生的变化。
func disableToolsTx(tx *gorm.DB, serverID string) ([]string, error) {
	var rows []toolRow
	if err := tx.Where("server_id = ? AND enabled != 0", serverID).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("查询待禁用工具失败: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	if err := tx.Model(&toolRow{}).
		Where("server_id = ? AND enabled != 0", serverID).
		Update("enabled", 0).Error; err != nil {
		return nil, fmt.Errorf("级联禁用工具失败: %w", err)
	}
	names := make([]string, 0, len(rows))
	for _, r := range rows {
		names = append(names, r.Name)
	}
	return names, nil
}

// SetCategory 设置 server 的单值分类（空串表示清除）。
func (s *Store) SetCategory(ctx context.Context, id, categoryID string) error {
	res := s.db.WithContext(ctx).Model(&serverRow{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"category_id": categoryID,
			"updated_at":  time.Now().UnixMilli(),
		})
	return storeutil.RequireAffected(res.RowsAffected, res.Error, "更新分类")
}

// Delete 删除 server，工具缓存与密钥随外键级联删除。
func (s *Store) Delete(ctx context.Context, id string) error {
	res := s.db.WithContext(ctx).Where("id = ?", id).Delete(&serverRow{})
	return storeutil.RequireAffected(res.RowsAffected, res.Error, "删除 server")
}

func scanServer(row serverRow, err error) (Server, error) {
	switch {
	case err == nil:
		return row.toServer(), nil
	case errors.Is(err, gorm.ErrRecordNotFound):
		return Server{}, storeutil.ErrNotFound
	default:
		return Server{}, fmt.Errorf("解析 server 行失败: %w", err)
	}
}
