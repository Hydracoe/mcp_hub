package servers

import (
	"context"
	"fmt"
	"time"

	"github.com/hydra/mcp-hub/internal/downstream"
	"github.com/hydra/mcp-hub/internal/store/storeutil"
	"github.com/hydra/mcp-hub/internal/store/toolchanges"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ProbeChangeStats 是一次探活对账产生的变更计数。
// 每轮探活后网关据此向运维打日志：上游迭代了什么，日志里要一眼可见，
// 不能只躺在 tool_changes 表里等人来查。
type ProbeChangeStats struct {
	Added   int `json:"added"`
	Updated int `json:"updated"`
	Removed int `json:"removed"`

	// AutoEnabled 是本轮新增里被"自动启用"放行的数量（O5）。
	// 它是 Added 的子集：默认策略下新工具禁用待人工，开了自动启用的
	// server 才会计到这里。分开设是因为两者的处置责任不同——
	// 默认路径要人来看，自动路径只要事后能查到就行。
	AutoEnabled int `json:"autoEnabled"`
}

// Total 返回变更总数。
func (s ProbeChangeStats) Total() int { return s.Added + s.Updated + s.Removed }

// SaveProbe 把探活结果写回 servers，成功时把工具缓存与最新清单对账。
// 探活失败只更新状态与错误信息，不动工具缓存——下游抖动不应让已聚合
// 的工具凭空消失。
//
// 对账语义（见 reconcileTools）：
//   - 新工具入库但默认禁用，须人工启用；创建向导勾选过的（引导名单内）
//     直接入库为启用；
//   - 已存在工具的 id 与启用状态保留，只刷新描述 / schema；
//   - 上游已删除的工具从缓存清掉，并写变更记录。
func (s *Store) SaveProbe(ctx context.Context, serverID string, res *downstream.ProbeResult) error {
	_, err := s.SaveProbeReported(ctx, serverID, res)
	return err
}

// InvalidateProbe 清掉一个 server 的探活结果与工具缓存。
//
// 只在"目标已经换了"时调用：旧工具属于另一个 URL/命令，继续留在缓存里
// 会被聚合层当成新目标的工具暴露出去，调用时带着旧工具名打到新下游。
// 探活失败不清缓存是另一条规则——同一个目标抖动时，上次已知的工具仍然有效。
func (s *Store) InvalidateProbe(ctx context.Context, serverID string) (int64, error) {
	var removed int64
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&serverRow{}).Where("id = ?", serverID).Updates(map[string]any{
			"status":          "unknown",
			"protocol_era":    "",
			"server_name":     "",
			"server_version":  "",
			"capabilities":    storeutil.JSONOrEmptySlice(nil),
			"instructions":    "",
			"last_check_at":   nil,
			"last_latency_ms": nil,
			"last_error_kind": "",
			"last_error":      "",
			"updated_at":      time.Now().UnixMilli(),
		})
		if err := storeutil.RequireAffected(res.RowsAffected, res.Error, "重置探活状态"); err != nil {
			return err
		}

		del := tx.Where("server_id = ?", serverID).Delete(&toolRow{})
		removed = del.RowsAffected
		return del.Error
	})
	if err != nil {
		return 0, err
	}
	return removed, nil
}

// SaveProbeReported 与 SaveProbe 相同，额外返回本轮对账的变更计数。
// 周期探活用它打"上游变了什么"的运维日志。
func (s *Store) SaveProbeReported(ctx context.Context, serverID string, res *downstream.ProbeResult) (ProbeChangeStats, error) {
	var stats ProbeChangeStats
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		stats, err = s.saveProbeTx(ctx, tx, serverID, res)
		return err
	})
	return stats, err
}

func (s *Store) saveProbeTx(ctx context.Context, tx *gorm.DB, serverID string, res *downstream.ProbeResult) (ProbeChangeStats, error) {
	// 工具缓存的 exposed_name 是 <key>__<tool>；引导名单也存在 servers
	// 表里，一次取齐。
	var row serverRow
	err := tx.Where("id = ?", serverID).Limit(1).Take(&row).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return ProbeChangeStats{}, storeutil.ErrNotFound
		}
		return ProbeChangeStats{}, fmt.Errorf("查询 server 失败: %w", err)
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
		"capabilities":    storeutil.JSONOrEmptySlice(res.Capabilities),
		"last_check_at":   res.CheckedAt.UnixMilli(),
		"last_latency_ms": res.Latency.Total,
		"last_error_kind": string(res.Kind),
		"last_error":      res.Detail,
		"updated_at":      time.Now().UnixMilli(),
	}
	// 协议版本与 instructions 只在非空时覆盖：空表示"这次没协商出来/没报上来"，
	// 抹掉已知的值会让下次连接回退到自动协商。
	if res.ProtocolVersion != "" {
		updates["protocol_version"] = res.ProtocolVersion
	}
	if res.Instructions != "" {
		updates["instructions"] = res.Instructions
	}

	if err := tx.Model(&serverRow{}).Where("id = ?", serverID).
		Updates(updates).Error; err != nil {
		return ProbeChangeStats{}, fmt.Errorf("更新探活结果失败: %w", err)
	}

	var stats ProbeChangeStats
	if res.OK {
		var err error
		stats, err = reconcileTools(tx, &row, res.Tools)
		if err != nil {
			return stats, err
		}
	}
	return stats, nil
}

// reconcileTools 把工具缓存与下游最新清单对账，并把差异写进 tool_changes。
//
// 刻意"按名对账"而不是整表删了重建：
//   - tools.id 必须稳定——tool_embeddings 以 tool_id 为键，每轮换 id
//     会让向量被无意义地全量重嵌（花 embedding 的钱）；
//   - enabled 是人工决定，整表重建会把它冲掉，"禁用一个工具"必须
//     跨探活周期存活。
func reconcileTools(tx *gorm.DB, sv *serverRow, tools []downstream.ToolInfo) (ProbeChangeStats, error) {
	// 既有行按名索引：id 与 enabled 都要从它们身上延续。
	var oldRows []toolRow
	if err := tx.Where("server_id = ?", sv.ID).Find(&oldRows).Error; err != nil {
		return ProbeChangeStats{}, fmt.Errorf("读取旧工具缓存失败: %w", err)
	}
	oldByName := make(map[string]toolRow, len(oldRows))
	for _, r := range oldRows {
		oldByName[r.Name] = r
	}

	live := make(map[string]struct{}, len(tools))
	for _, t := range tools {
		live[t.Name] = struct{}{}
	}

	now := time.Now()
	changes := make([]toolchanges.Change, 0)
	autoEnabled := 0

	// 1) 删除上游已不存在的工具。
	// 清单为空时不能拼 `NOT IN ()`：SQL 里 `x NOT IN (NULL)` 恒为
	// UNKNOWN，会一行都不删——"上游把工具全撤了"恰恰必须全删。
	del := tx.Where("server_id = ?", sv.ID)
	if len(tools) > 0 {
		del = del.Where("name NOT IN ?", namesOf(tools))
	}
	if err := del.Delete(&toolRow{}).Error; err != nil {
		return ProbeChangeStats{}, fmt.Errorf("清理已删除工具缓存失败: %w", err)
	}
	for _, old := range oldRows {
		if _, stillLive := live[old.Name]; stillLive {
			continue
		}
		changes = append(changes, toolchanges.Change{
			ServerID:  sv.ID,
			ServerKey: sv.Key,
			ToolName:  old.Name,
			Action:    toolchanges.ActionRemoved,
			Actor:     toolchanges.ActorSystem,
			TS:        now,
		})
	}

	if len(tools) == 0 {
		return writeChanges(tx, sv, changes)
	}

	// 2) Upsert 最新清单：新行按引导名单给 enabled，旧行只刷元数据、
	//    不动 id 与 enabled。冲突键是 (server_id, name) 唯一约束。
	//
	// 引导名单（boot_enabled_tools）来自创建向导的勾选，只对"新行"生效
	// 一次，消费后即清空。旧行的 enabled 是人工决定，任何探活都不能覆盖
	// ——否则"禁用一个工具"撑不过下一个探活周期。
	boot := make(map[string]struct{})
	if names := storeutil.DecodeStringSlice(sv.BootEnabledTools); len(names) > 0 {
		for _, n := range names {
			boot[n] = struct{}{}
		}
		// 名单只服务本轮：无论命中多少，都算已消费。写库与工具行同事务，
		// 探活回滚时名单也一并回滚，不会出现"消费了但没落库"。
		sv.BootEnabledTools = "[]"
		if err := tx.Model(&serverRow{}).Where("id = ?", sv.ID).
			Update("boot_enabled_tools", "[]").Error; err != nil {
			return ProbeChangeStats{}, fmt.Errorf("清理引导名单失败: %w", err)
		}
	}

	rows := make([]toolRow, 0, len(tools))
	for _, t := range tools {
		schema := string(t.InputSchema)
		if schema == "" {
			schema = "{}"
		}

		if old, ok := oldByName[t.Name]; ok {
			// 描述或参数定义变了：留一条 updated，方便回答"AI 怎么突然
			// 换了参数"——行本身仍是同一个工具，id 不变。
			if old.Description != t.Description || old.InputSchema != schema {
				changes = append(changes, toolchanges.Change{
					ServerID:  sv.ID,
					ServerKey: sv.Key,
					ToolName:  t.Name,
					Action:    toolchanges.ActionUpdated,
					Detail:    "工具描述或参数定义已随上游更新",
					Actor:     toolchanges.ActorSystem,
					TS:        now,
				})
			}
			rows = append(rows, toolRow{
				ID:          old.ID,
				ServerID:    sv.ID,
				Name:        t.Name,
				ExposedName: sv.Key + "__" + t.Name,
				Description: t.Description,
				InputSchema: schema,
				Enabled:     old.Enabled,
				CachedMS:    now.UnixMilli(),
			})
			continue
		}

		// 新行的启用判据有两条来源，优先级明确：
		//   1) 引导名单（创建向导勾选）——一次性，只对这一轮新行生效；
		//   2) server 的自动启用开关（O5）——持续性策略，默认关闭。
		// 两者都只作用于**新发现**的工具：已存在的行无论启用与否都保留
		// 原值，否则"人工禁用过的工具"撑不过下一个探活周期。
		enabled := int64(0)
		why := addedWhyDefault
		switch {
		case hasBool(boot, t.Name):
			enabled = 1
			why = addedWhyBoot
		case sv.AutoEnable != 0:
			enabled = 1
			why = addedWhyAuto
			autoEnabled++
		}
		rows = append(rows, toolRow{
			ID:          storeutil.NewID(),
			ServerID:    sv.ID,
			Name:        t.Name,
			ExposedName: sv.Key + "__" + t.Name,
			Description: t.Description,
			InputSchema: schema,
			Enabled:     enabled,
			CachedMS:    now.UnixMilli(),
		})
		changes = append(changes, toolchanges.Change{
			ServerID:  sv.ID,
			ServerKey: sv.Key,
			ToolName:  t.Name,
			Action:    toolchanges.ActionAdded,
			Detail:    addedDetail(why),
			Actor:     toolchanges.ActorSystem,
			TS:        now,
		})
	}

	if err := tx.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "server_id"}, {Name: "name"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"exposed_name", "description", "input_schema", "cached_at",
		}),
	}).CreateInBatches(&rows, 200).Error; err != nil {
		return ProbeChangeStats{}, fmt.Errorf("写入工具缓存失败(%d 个): %w", len(rows), err)
	}

	stats, err := writeChanges(tx, sv, changes)
	stats.AutoEnabled = autoEnabled
	return stats, err
}

// writeChanges 落盘本轮差异，并按动作类型归集计数。
func writeChanges(tx *gorm.DB, sv *serverRow, changes []toolchanges.Change) (ProbeChangeStats, error) {
	stats := ProbeChangeStats{}
	for i := range changes {
		if err := toolchanges.InsertTx(tx, changes[i]); err != nil {
			return stats, err
		}
		switch changes[i].Action {
		case toolchanges.ActionAdded:
			stats.Added++
		case toolchanges.ActionUpdated:
			stats.Updated++
		case toolchanges.ActionRemoved:
			stats.Removed++
		}
	}
	return stats, nil
}

func namesOf(tools []downstream.ToolInfo) []string {
	out := make([]string, 0, len(tools))
	for _, t := range tools {
		out = append(out, t.Name)
	}
	return out
}

// addedWhy 是新工具被写成"启用"的原因，决定变更记录里那句话怎么说。
// 三句话必须能区分：默认禁用是要人来做决定，另两种是"网关替你决定了，
// 原因写在这儿"——混成一句"直接启用"，事后没法回答"谁批准它上线的"。
type addedWhy int

const (
	addedWhyDefault addedWhy = iota // 默认禁用，需人工启用
	addedWhyBoot                    // 创建向导勾选
	addedWhyAuto                    // server 开了自动启用
)

func addedDetail(why addedWhy) string {
	switch why {
	case addedWhyBoot:
		return "新发现的工具，按创建时的勾选直接启用"
	case addedWhyAuto:
		return "新发现的工具，按该 server 的自动启用策略直接启用（如需收回，请在工具列表逐个禁用）"
	default:
		return "新发现的工具，默认禁用，需人工启用"
	}
}

// hasBool 查 set 里有没有这个键。
func hasBool(set map[string]struct{}, key string) bool {
	_, ok := set[key]
	return ok
}

// SetToolEnabled 启用或禁用单个工具。
// 返回 false 表示该 server 下没有这个工具（上层映射 404）。
func (s *Store) SetToolEnabled(ctx context.Context, serverID, toolName string, enabled bool) (bool, error) {
	res := s.db.WithContext(ctx).Model(&toolRow{}).
		Where("server_id = ? AND name = ?", serverID, toolName).
		Update("enabled", storeutil.BoolToInt(enabled))
	if res.Error != nil {
		return false, fmt.Errorf("更新工具启用状态失败: %w", res.Error)
	}
	return res.RowsAffected == 1, nil
}

// CountEnabledTools 统计某 server 当前启用的工具数。
// 供「server 没有可用工具就自动停用」的约束检查使用。
func (s *Store) CountEnabledTools(ctx context.Context, serverID string) (int, error) {
	var n int64
	err := s.db.WithContext(ctx).Model(&toolRow{}).
		Where("server_id = ? AND enabled = 1", serverID).
		Count(&n).Error
	if err != nil {
		return 0, fmt.Errorf("统计启用工具数失败: %w", err)
	}
	return int(n), nil
}

// HasTool 判断某 server 下是否存在指定工具（不论启用与否）。
func (s *Store) HasTool(ctx context.Context, serverID, toolName string) (bool, error) {
	var n int64
	err := s.db.WithContext(ctx).Model(&toolRow{}).
		Where("server_id = ? AND name = ?", serverID, toolName).
		Count(&n).Error
	if err != nil {
		return false, fmt.Errorf("查询工具是否存在失败: %w", err)
	}
	return n > 0, nil
}

// CountEnabledWithoutTools 统计"启用中但没有任何启用工具"的 server 数。
// 停用 server 会级联禁用其工具，重新启用不会自动恢复——这类空转状态
// （探活照跑、AI 侧却什么都看不到）必须在概览与列表上显性提示。
func (s *Store) CountEnabledWithoutTools(ctx context.Context) (int, error) {
	var n int64
	err := s.db.WithContext(ctx).Model(&serverRow{}).
		Where("enabled = 1 AND NOT EXISTS (SELECT 1 FROM tools t WHERE t.server_id = servers.id AND t.enabled = 1)").
		Count(&n).Error
	if err != nil {
		return 0, fmt.Errorf("统计无生效工具的启用 server 失败: %w", err)
	}
	return int(n), nil
}

// MarkNoUsableKey 把一个 server 标为 degraded（原因：没有可用的密钥）。
// 不用 offline：服务是通的，只是凭据全不可用，两者的排查方向完全不同。
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
