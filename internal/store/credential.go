package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hydra/mcp-hub/internal/downstream"
	"gorm.io/gorm"
)

// 凭据的持久状态。只有两种——冷却（cooling）刻意不进数据库。
//
// 理由：重启往往就是为了从那一次限流里恢复，把限流状态持久化只会让
// 新进程继续背旧账。反过来，"这把密钥无效"是稳定事实，重启后不该
// 又拿它去打一次。
const (
	CredStateActive   = "active"
	CredStateDisabled = "disabled"
)

// ErrCredentialExists 表示同一 server 下已存在同名 label。
var ErrCredentialExists = errors.New("credential label already in use")

// Credential 是 credentials 表的一行。
//
// 一行 = 一组完整的注入头，而不是"某个头字段"。供应商的凭据形态各异
// （Bearer / X-API-Key / 多字段签名），只有整组才是一次可独立轮换的
// 最小单位；按字段拆开会让"换掉其中一把"这件事变得无法表达。
type Credential struct {
	ID       string
	ServerID string
	Label    string
	Headers  map[string]string

	// Enabled 是人工总开关，State 是自动判决的结果。两者正交：
	// 用户把一把密钥关掉（Enabled=false）与它被判定无效（State=disabled）
	// 是不同的事实，界面上也该分开显示。
	Enabled bool

	State        string
	DisabledKind downstream.KeyVerdict
	DisabledAt   time.Time
	DisabledNote string

	// 0 表示不限。
	RPMLimit int
	TPMLimit int

	SortOrder int
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Usable 报告这把密钥是否参与轮换。
//
// 只看持久层能知道的部分；冷却与配额余量是进程内状态，
// 由密钥池叠加判断。
func (c Credential) Usable() bool {
	return c.Enabled && c.State != CredStateDisabled
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

// ListCredentials 返回某个 server 的全部密钥，按 sort_order 再按创建时间排序。
func (s *Store) ListCredentials(ctx context.Context, serverID string) ([]Credential, error) {
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

// ListAllCredentials 返回全部密钥，按 server 分组由调用方处理。
//
// 存在是为了让密钥池装载时一次读完，而不是按 server 逐个查（典型 N+1）。
func (s *Store) ListAllCredentials(ctx context.Context) (map[string][]Credential, error) {
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

// GetCredential 按 id 查询。
func (s *Store) GetCredential(ctx context.Context, id string) (Credential, error) {
	var row credentialRow
	err := s.db.WithContext(ctx).Where("id = ?", id).Take(&row).Error
	switch {
	case err == nil:
		return row.toCredential(), nil
	case errors.Is(err, gorm.ErrRecordNotFound):
		return Credential{}, ErrNotFound
	default:
		return Credential{}, fmt.Errorf("解析密钥行失败: %w", err)
	}
}

// InsertCredential 新增一把密钥，追加到轮换顺序的末尾。ID 为空时自动生成。
//
// label 在同一 server 内唯一：界面上两行都叫"主号"的密钥，用户
// 无法在日志与界面之间对上号。
//
// sort_order 由这里定，不取调用方的值：新密钥插到 0 会**静默改变
// 所有既有密钥的轮换优先级**——用户只是想再加一把，结果原来那把
// 从"每两次用一次"变成"每三次用一次"，而没有任何迹象表明发生过
// 这件事。要调整顺序请用 UpdateCredential。
func (s *Store) InsertCredential(ctx context.Context, c Credential) (Credential, error) {
	if c.ID == "" {
		c.ID = NewID()
	}
	if c.State == "" {
		c.State = CredStateActive
	}
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
		Headers:  jsonOrEmptyMap(c.Headers),
		Enabled:  boolToInt(c.Enabled),
		State:    c.State,
		RPMLimit: int64(c.RPMLimit),
		TPMLimit: int64(c.TPMLimit),
		// disabled_* 一律以空/NULL 起始：新密钥没有被判决过。
		SortOrder: next,
		CreatedMS: now,
		UpdatedMS: now,
	}
	if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
		if isUniqueViolation(err) {
			return Credential{}, fmt.Errorf("%w: %q", ErrCredentialExists, c.Label)
		}
		return Credential{}, fmt.Errorf("新增密钥失败: %w", err)
	}
	return s.GetCredential(ctx, c.ID)
}

// UpdateCredential 更新一把密钥的可编辑字段。
//
// 刻意不碰 state / disabled_* ：状态是判决结果，改配置不该顺手把它
// 复位——那会让"这把密钥为什么被禁"的线索凭空消失。
func (s *Store) UpdateCredential(ctx context.Context, c Credential) error {
	if err := s.checkLabelFree(ctx, c.ServerID, c.Label, c.ID); err != nil {
		return err
	}
	res := s.db.WithContext(ctx).Model(&credentialRow{}).
		Where("id = ?", c.ID).
		Updates(map[string]any{
			"label":      c.Label,
			"headers":    jsonOrEmptyMap(c.Headers),
			"rpm_limit":  int64(c.RPMLimit),
			"tpm_limit":  int64(c.TPMLimit),
			"sort_order": int64(c.SortOrder),
			"updated_at": time.Now().UnixMilli(),
		})
	if res.Error != nil {
		if isUniqueViolation(res.Error) {
			return fmt.Errorf("%w: %q", ErrCredentialExists, c.Label)
		}
		return fmt.Errorf("更新密钥失败: %w", res.Error)
	}
	return requireAffected(res.RowsAffected, nil, "更新密钥")
}

// DeleteCredential 删除一把密钥。
func (s *Store) DeleteCredential(ctx context.Context, id string) error {
	res := s.db.WithContext(ctx).Where("id = ?", id).Delete(&credentialRow{})
	return requireAffected(res.RowsAffected, res.Error, "删除密钥")
}

// SetCredentialEnabled 人工启停一把密钥。
//
// 重新启用会顺带把自动判决的状态复位：用户点"启用"的意图是
// "我要用它"，此时残留一个 disabled 状态会让这个动作静默失效。
func (s *Store) SetCredentialEnabled(ctx context.Context, id string, enabled bool) error {
	now := time.Now().UnixMilli()
	res := s.db.WithContext(ctx).Model(&credentialRow{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"enabled":       boolToInt(enabled),
			"state":         CredStateActive,
			"disabled_kind": "",
			"disabled_at":   nil,
			"disabled_note": "",
			"updated_at":    now,
		})
	return requireAffected(res.RowsAffected, res.Error, "更新密钥启停")
}

// DisableCredential 因自动判决而弃用一把密钥。
//
// 与 SetCredentialEnabled 分开：人工操作会复位状态，自动弃用则要
// 把"为什么被禁"记下来。合成一个方法会让调用方必须记得传 nil kind，
// 那种"忘了就静默丢信息"的接口迟早出错。
func (s *Store) DisableCredential(ctx context.Context, id string, kind downstream.KeyVerdict, note string) error {
	now := time.Now().UnixMilli()
	res := s.db.WithContext(ctx).Model(&credentialRow{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"state":         CredStateDisabled,
			"disabled_kind": string(kind),
			"disabled_at":   now,
			"disabled_note": note,
			"updated_at":    now,
		})
	return requireAffected(res.RowsAffected, res.Error, "弃用密钥")
}

// CountCredentials 返回某个 server 的密钥数与可用密钥数。
//
// "可用"只算持久层能判断的：已启用且未被弃用。冷却与配额余量
// 在内存里，不在这里。
func (s *Store) CountCredentials(ctx context.Context, serverID string) (total, usable int, err error) {
	row := s.db.WithContext(ctx).Model(&credentialRow{}).
		Select(`COUNT(*),
		        COALESCE(SUM(CASE WHEN enabled = 1 AND state <> ? THEN 1 ELSE 0 END), 0)`,
			CredStateDisabled).
		Where("server_id = ?", serverID).
		Row()
	if err := row.Scan(&total, &usable); err != nil {
		return 0, 0, fmt.Errorf("统计密钥失败: %w", err)
	}
	return total, usable, nil
}

// CountAllUsableCredentials 返回全部 server 的可用密钥数，键为 server_id。
func (s *Store) CountAllUsableCredentials(ctx context.Context) (map[string]int, error) {
	var rows []struct {
		ServerID string
		N        int64
	}
	err := s.db.WithContext(ctx).Model(&credentialRow{}).
		Select("server_id, COUNT(*) AS n").
		Where("enabled = 1 AND state <> ?", CredStateDisabled).
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

// InsertCredentials 批量新增密钥（追加语义，不动已有那些）。
//
// 追加而不是整体替换：用户粘贴 10 行密钥，期望的是"再加 10 把"，
// 而不是"把已有的 3 把删掉、换成这 10 把"。整体替换那种语义需要
// 用户先确认一次"我现在的池子里有什么"，代价高得多。
//
// 事务内完成：导入到一半失败会留下一个既不是旧集合也不是新集合的
// 密钥池，而那种状态下"哪把能用"没人说得清。
func (s *Store) InsertCredentials(ctx context.Context, serverID string, creds []Credential) ([]Credential, error) {
	if len(creds) == 0 {
		return nil, nil
	}

	// 先读出已有 label，让重复名在事务开始前就被挡住，
	// 也好给出"哪一行重了"这种可操作的错误。
	existing, err := s.ListCredentials(ctx, serverID)
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
			c.ID = NewID()
		}
		if c.State == "" {
			c.State = CredStateActive
		}
		c.ServerID = serverID
		c.SortOrder = base + i
		c.CreatedAt = time.UnixMilli(now)
		c.UpdatedAt = c.CreatedAt

		rows = append(rows, credentialRow{
			ID:        c.ID,
			ServerID:  serverID,
			Label:     c.Label,
			Headers:   jsonOrEmptyMap(c.Headers),
			Enabled:   boolToInt(c.Enabled),
			State:     c.State,
			RPMLimit:  int64(c.RPMLimit),
			TPMLimit:  int64(c.TPMLimit),
			SortOrder: int64(c.SortOrder),
			CreatedMS: now,
			UpdatedMS: now,
		})
		out = append(out, c)
	}

	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.CreateInBatches(&rows, 200).Error; err != nil {
			if isUniqueViolation(err) {
				// 走到这里说明预检之后有并发写入抢了同一个 label，
				// 具体是哪一行已经说不清了，让用户重试一次导入。
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

// checkLabelFree 检查同一 server 内 label 是否已被别的密钥占用。
//
// 先查一次再写，是为了给出可读错误。真正的约束由唯一索引兜底，
// 并发写入仍可能撞车，因此 INSERT/UPDATE 的 unique violation 也要捕获。
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
