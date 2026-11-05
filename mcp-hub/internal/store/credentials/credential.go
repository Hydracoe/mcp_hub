// Package credentials 是 credentials 表的持久化：下游密钥池。
// 一行 = 一组完整的注入头（一次可独立轮换的最小单位）。
package credentials

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hydra/mcp-hub/internal/downstream"
	"github.com/hydra/mcp-hub/internal/store/storeutil"

	"gorm.io/gorm"
)

// CredState 是密钥的持久状态。cooling（限流冷却）刻意不进数据库：重启往往
// 就是为了从限流中恢复，落库只会让新进程背旧账；disabled 是稳定事实，重启后
// 不该再拿无效密钥去打一次。用枚举类型而非裸 string，防止任意字面量落库。
type CredState string

const (
	// CredStateActive 表示未被判决过，或人工重新启用过。
	CredStateActive CredState = "active"
	// CredStateDisabled 表示已被自动判决弃用，只有人工能救。
	CredStateDisabled CredState = "disabled"
)

// OrActive 把零值归位成 active：库里带默认值的存量行，以及调用方用零值
// 构造的 Credential（新增密钥没被判决过）。不认识的取值不归位——
// 那是脏数据，宁可让它在界面上显形，也不要被悄悄改写成 active。
func (s CredState) OrActive() CredState {
	if s == "" {
		return CredStateActive
	}
	return s
}

// KeyState 把持久状态映射成运行态枚举（downstream.KeyState）。
// 白名单：只有 CredStateActive 算可用，其余一律 KeyDisabled。持久层只知道
// "是否被弃用"，cooling 由密钥池在内存里叠加；认不出的取值宁可不发出去。
func (s CredState) KeyState() downstream.KeyState {
	if s.OrActive() == CredStateActive {
		return downstream.KeyActive
	}
	return downstream.KeyDisabled
}

// ErrCredentialExists 表示同一 server 下已存在同名 label。
// 文案直接面向用户（toast 描述），保持中文与可读。
var ErrCredentialExists = errors.New("同一 Server 下的密钥标签不能重复")

// credentialRow 是 credentials 表的一行（持久化形态）。
type credentialRow struct {
	ID       string `gorm:"column:id;primaryKey"`
	ServerID string `gorm:"column:server_id"`
	Label    string `gorm:"column:label"`
	Headers  string `gorm:"column:headers"` // JSON object
	Enabled  int64  `gorm:"column:enabled"`

	// 持久状态只有 active / disabled：cooling 是运行态，但冷却的到点时刻会落库（O4），
	// 用于重启后恢复内存态。它只是"恢复内存态用的事实"，不是第二种状态：过期值按不存在处理。
	State        CredState `gorm:"column:state"`
	DisabledKind string    `gorm:"column:disabled_kind"`
	DisabledAt   *int64    `gorm:"column:disabled_at"`
	DisabledNote string    `gorm:"column:disabled_note"`
	CoolingUntil *int64    `gorm:"column:cooling_until"`

	RPMLimit int64 `gorm:"column:rpm_limit"`

	SortOrder int64 `gorm:"column:sort_order"`
	CreatedMS int64 `gorm:"column:created_at"`
	UpdatedMS int64 `gorm:"column:updated_at"`
}

func (credentialRow) TableName() string { return "credentials" }

func (r credentialRow) toCredential() Credential {
	c := Credential{
		ID:           r.ID,
		ServerID:     r.ServerID,
		Label:        r.Label,
		Headers:      storeutil.DecodeStringMap(r.Headers),
		Enabled:      storeutil.IntToBool(r.Enabled),
		State:        r.State.OrActive(),
		DisabledKind: downstream.KeyVerdict(r.DisabledKind),
		DisabledNote: r.DisabledNote,
		RPMLimit:     int(r.RPMLimit),
		SortOrder:    int(r.SortOrder),
		CreatedAt:    time.UnixMilli(r.CreatedMS),
		UpdatedAt:    time.UnixMilli(r.UpdatedMS),
	}
	if r.DisabledAt != nil {
		c.DisabledAt = time.UnixMilli(*r.DisabledAt)
	}
	if r.CoolingUntil != nil {
		c.CoolingUntil = time.UnixMilli(*r.CoolingUntil)
	}
	return c
}

// Credential 是 credentials 表的一行（领域形态）。
type Credential struct {
	ID       string
	ServerID string
	Label    string
	Headers  map[string]string

	// Enabled 是人工总开关，State 是自动判决的结果，两者正交、分开显示。
	Enabled bool

	State        CredState
	DisabledKind downstream.KeyVerdict
	DisabledAt   time.Time
	DisabledNote string

	// CoolingUntil 是冷却到点时刻（O4），零值表示未在冷却。
	// 只用于启动时恢复内存态；调用路径上的冷却判断一律以内存为准（库里的值可能已过期）。
	CoolingUntil time.Time

	// 0 表示不限。
	RPMLimit int

	SortOrder int
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Usable 报告这把密钥是否参与轮换。只看持久层能知道的部分；冷却与配额余量
// 由密钥池叠加判断。判据与 CredState.KeyState 同口径（白名单）：
// 只有"人工打开 + 状态是 active"才放行，认不出的状态不当可用。
func (c Credential) Usable() bool {
	return c.Enabled && c.State.KeyState() == downstream.KeyActive
}

// LabelOrFallback 返回展示用名称。
func (c Credential) LabelOrFallback() string {
	if strings.TrimSpace(c.Label) != "" {
		return c.Label
	}
	if len(c.ID) <= 8 {
		return "密钥 " + c.ID
	}
	return "密钥 " + c.ID[:8]
}

// Store 是 credentials 表的读写入口。
type Store struct {
	db *gorm.DB
}

// New 构造 credentials 包的读写入口。
func New(db *gorm.DB) *Store { return &Store{db: db} }

// List 返回某个 server 的全部密钥，按 sort_order 再按创建时间排序。
func (s *Store) List(ctx context.Context, serverID string) ([]Credential, error) {
	var rows []credentialRow
	err := s.db.WithContext(ctx).
		Where("server_id = ?", serverID).
		Order("sort_order, created_at, id").
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("查询密钥列表失败: %w", err)
	}
	return toCredentials(rows), nil
}

// ListAll 返回全部密钥（按 server 分组）。
// 密钥池装载时一次读完
func (s *Store) ListAll(ctx context.Context) (map[string][]Credential, error) {
	var rows []credentialRow
	err := s.db.WithContext(ctx).
		Order("server_id, sort_order, created_at, id").
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("查询密钥列表失败: %w", err)
	}

	out := make(map[string][]Credential)
	for _, r := range rows {
		c := r.toCredential()
		out[c.ServerID] = append(out[c.ServerID], c)
	}
	return out, nil
}

// Get 按 id 查询。
func (s *Store) Get(ctx context.Context, id string) (Credential, error) {
	var row credentialRow
	err := s.db.WithContext(ctx).Where("id = ?", id).Take(&row).Error
	switch {
	case err == nil:
		return row.toCredential(), nil
	case errors.Is(err, gorm.ErrRecordNotFound):
		return Credential{}, storeutil.ErrNotFound
	default:
		return Credential{}, fmt.Errorf("解析密钥行失败: %w", err)
	}
}

// GetByIDs 按 id 批量查询，用于日志等引用了密钥 id 的展示场景。
// 已删除的 id 不在返回里（Find 对缺行不报错），调用方自行兜底展示原始 id。
func (s *Store) GetByIDs(ctx context.Context, ids []string) ([]Credential, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var rows []credentialRow
	err := s.db.WithContext(ctx).Where("id IN ?", ids).Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("批量查询密钥失败: %w", err)
	}
	return toCredentials(rows), nil
}

// Insert 新增一把密钥，追加到轮换顺序的末尾。ID 为空时自动生成。
// sort_order 由这里定：新密钥插到 0 会静默改变既有密钥的轮换优先级，
// 调整顺序请用 Update。
func (s *Store) Insert(ctx context.Context, c Credential) (Credential, error) {
	if c.ID == "" {
		c.ID = storeutil.NewID()
	}
	c.State = c.State.OrActive()
	if err := s.checkLabelFree(ctx, c.ServerID, c.Label, c.ID); err != nil {
		return Credential{}, err
	}

	next, err := s.nextSortOrder(ctx, c.ServerID)
	if err != nil {
		return Credential{}, err
	}

	now := time.Now().UnixMilli()
	row := credentialRow{
		ID:       c.ID,
		ServerID: c.ServerID,
		Label:    c.Label,
		Headers:  storeutil.JSONOrEmptyMap(c.Headers),
		Enabled:  storeutil.BoolToInt(c.Enabled),
		State:    c.State,
		RPMLimit: int64(c.RPMLimit),
		// disabled_* 一律以空/NULL 起始：新密钥没有被判决过。
		SortOrder: next,
		CreatedMS: now,
		UpdatedMS: now,
	}
	if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
		if storeutil.IsUniqueViolation(err) {
			return Credential{}, fmt.Errorf("%w: %q", ErrCredentialExists, c.Label)
		}
		return Credential{}, fmt.Errorf("新增密钥失败: %w", err)
	}
	return s.Get(ctx, c.ID)
}

// Update 更新一把密钥的可编辑字段。
// 刻意不碰 state / disabled_*：那是自动判决结果，改配置不该把它复位，
// 否则"这把密钥为什么被禁"的线索就丢了。
func (s *Store) Update(ctx context.Context, c Credential) error {
	if err := s.checkLabelFree(ctx, c.ServerID, c.Label, c.ID); err != nil {
		return err
	}
	res := s.db.WithContext(ctx).Model(&credentialRow{}).
		Where("id = ?", c.ID).
		Updates(map[string]any{
			"label":      c.Label,
			"headers":    storeutil.JSONOrEmptyMap(c.Headers),
			"rpm_limit":  int64(c.RPMLimit),
			"sort_order": int64(c.SortOrder),
			"updated_at": time.Now().UnixMilli(),
		})
	if res.Error != nil {
		if storeutil.IsUniqueViolation(res.Error) {
			return fmt.Errorf("%w: %q", ErrCredentialExists, c.Label)
		}
		return fmt.Errorf("更新密钥失败: %w", res.Error)
	}
	return storeutil.RequireAffected(res.RowsAffected, nil, "更新密钥")
}

// Delete 删除一把密钥。
func (s *Store) Delete(ctx context.Context, id string) error {
	res := s.db.WithContext(ctx).Where("id = ?", id).Delete(&credentialRow{})
	return storeutil.RequireAffected(res.RowsAffected, res.Error, "删除密钥")
}

// SetEnabled 人工启停一把密钥。
// 重新启用会顺带复位自动判决状态：用户点"启用"的意图是"我要用它"，
// 残留 disabled 会让这个动作静默失效。
func (s *Store) SetEnabled(ctx context.Context, id string, enabled bool) error {
	now := time.Now().UnixMilli()
	res := s.db.WithContext(ctx).Model(&credentialRow{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"enabled":       storeutil.BoolToInt(enabled),
			"state":         CredStateActive,
			"disabled_kind": "",
			"disabled_at":   nil,
			"disabled_note": "",
			// 人工救回一并清冷却：留着旧冷却等于"救回来但还在关禁闭"。
			"cooling_until": nil,
			"updated_at":    now,
		})
	return storeutil.RequireAffected(res.RowsAffected, res.Error, "更新密钥启停")
}

// SetCoolingUntil 记录冷却到点时刻（O4），只在启动恢复时读一次，之后冷却推进在内存里。
// 写它是为了让下一次启动知道"这把钥匙此刻还在被限"，传零值表示清掉。
func (s *Store) SetCoolingUntil(ctx context.Context, id string, until time.Time) error {
	var v any
	if !until.IsZero() {
		v = until.UnixMilli()
	}
	res := s.db.WithContext(ctx).Model(&credentialRow{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"cooling_until": v,
			"updated_at":    time.Now().UnixMilli(),
		})
	// 密钥可能刚被删掉：写 0 行不是错误，静默放过。
	if res.Error != nil {
		return fmt.Errorf("记录密钥冷却失败: %w", res.Error)
	}
	return nil
}

// Disable 因自动判决弃用一把密钥，记下"为什么被禁"。
// 与 SetEnabled 分开：人工操作复位状态，自动弃用记录原因。
func (s *Store) Disable(ctx context.Context, id string, kind downstream.KeyVerdict, note string) error {
	now := time.Now().UnixMilli()
	res := s.db.WithContext(ctx).Model(&credentialRow{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"state":         CredStateDisabled,
			"disabled_kind": string(kind),
			"disabled_at":   now,
			"disabled_note": note,
			// 弃用覆盖冷却：弃用是更重的判决，留着冷却时间只会让
			// "重启后恢复冷却"把一个已弃用的密钥提前放回轮换。
			"cooling_until": nil,
			"updated_at":    now,
		})
	return storeutil.RequireAffected(res.RowsAffected, res.Error, "弃用密钥")
}

// usableStateCond 是"持久层认为可用"的 SQL 判据，必须与 Credential.Usable /
// CredState.KeyState 同口径（白名单 + 空值归位）：否则界面上的"可用密钥数"
// 会比密钥池真正轮换的多。
const usableStateCond = `enabled = 1 AND (state = ? OR state = '')`

// Count 返回某个 server 的密钥数与可用密钥数
// （"可用"只算持久层能判断的：已启用且状态是 active；冷却与配额在内存里）。
func (s *Store) Count(ctx context.Context, serverID string) (total, usable int, err error) {
	row := s.db.WithContext(ctx).Model(&credentialRow{}).
		Select(`COUNT(*),COALESCE(SUM(CASE WHEN `+usableStateCond+` THEN 1 ELSE 0 END), 0)`, CredStateActive).
		Where("server_id = ?", serverID).
		Row()
	if err := row.Scan(&total, &usable); err != nil {
		return 0, 0, fmt.Errorf("统计密钥失败: %w", err)
	}
	return total, usable, nil
}

// CountAllUsable 返回全部 server 的可用密钥数，键为 server_id。
func (s *Store) CountAllUsable(ctx context.Context) (map[string]int, error) {
	var rows []struct {
		ServerID string
		N        int64
	}
	err := s.db.WithContext(ctx).Model(&credentialRow{}).
		Select("server_id, COUNT(*) AS n").
		Where(usableStateCond, CredStateActive).
		Group("server_id").
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("统计密钥失败: %w", err)
	}

	out := make(map[string]int, len(rows))
	for _, r := range rows {
		out[r.ServerID] = int(r.N)
	}
	return out, nil
}

// InsertBatch 批量新增密钥（追加语义，事务内完成）：用户粘贴 10 行密钥，
// 期望的是"再加 10 把"；导入到一半失败不能留下不新不旧的密钥池。
func (s *Store) InsertBatch(ctx context.Context, serverID string, creds []Credential) ([]Credential, error) {
	if len(creds) == 0 {
		return nil, nil
	}

	// 先读已有 label，让重复名在事务开始前被挡住，并给出"哪一行重了"。
	existing, err := s.List(ctx, serverID)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(existing)+len(creds))
	for _, c := range existing {
		if c.Label != "" {
			seen[c.Label] = struct{}{}
		}
	}
	for _, c := range creds {
		if c.Label == "" {
			continue
		}
		if _, dup := seen[c.Label]; dup {
			return nil, fmt.Errorf("%w: %q", ErrCredentialExists, c.Label)
		}
		seen[c.Label] = struct{}{}
	}

	now := time.Now().UnixMilli()
	base := 0
	if n := len(existing); n > 0 {
		base = existing[n-1].SortOrder + 1
	}

	out := make([]Credential, 0, len(creds))
	rows := make([]credentialRow, 0, len(creds))
	for i, c := range creds {
		if c.ID == "" {
			c.ID = storeutil.NewID()
		}
		c.State = c.State.OrActive()
		c.ServerID = serverID
		c.SortOrder = base + i
		c.CreatedAt = time.UnixMilli(now)
		c.UpdatedAt = c.CreatedAt

		rows = append(rows, credentialRow{
			ID:        c.ID,
			ServerID:  serverID,
			Label:     c.Label,
			Headers:   storeutil.JSONOrEmptyMap(c.Headers),
			Enabled:   storeutil.BoolToInt(c.Enabled),
			State:     c.State,
			RPMLimit:  int64(c.RPMLimit),
			SortOrder: int64(c.SortOrder),
			CreatedMS: now,
			UpdatedMS: now,
		})
		out = append(out, c)
	}

	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.CreateInBatches(&rows, 200).Error; err != nil {
			if storeutil.IsUniqueViolation(err) {
				// 预检之后有并发写入抢了同一个 label，让用户重试导入。
				return ErrCredentialExists
			}
			return fmt.Errorf("导入密钥失败: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// nextSortOrder 返回该 server 下一个轮换序号。
func (s *Store) nextSortOrder(ctx context.Context, serverID string) (int64, error) {
	var next int64
	row := s.db.WithContext(ctx).Model(&credentialRow{}).
		Select("COALESCE(MAX(sort_order), -1) + 1").
		Where("server_id = ?", serverID).
		Row()
	if err := row.Scan(&next); err != nil {
		return 0, fmt.Errorf("计算密钥顺序失败: %w", err)
	}
	return next, nil
}

// checkLabelFree 检查同一 server 内 label 是否已被占用（可读错误，
// 并发竞态由唯一索引兜底）。空 label 不参与约束。
func (s *Store) checkLabelFree(ctx context.Context, serverID, label, selfID string) error {
	if strings.TrimSpace(label) == "" {
		return nil
	}
	var holders []string
	err := s.db.WithContext(ctx).Model(&credentialRow{}).
		Where("server_id = ? AND label = ?", serverID, label).
		Limit(1).Pluck("id", &holders).Error
	if err != nil {
		return fmt.Errorf("检查密钥名冲突失败: %w", err)
	}
	if len(holders) > 0 && holders[0] != selfID {
		return fmt.Errorf("%w: %q", ErrCredentialExists, label)
	}
	return nil
}

func toCredentials(rows []credentialRow) []Credential {
	out := make([]Credential, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.toCredential())
	}
	return out
}
