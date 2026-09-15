package store

import (
	"context"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/hydra/mcp-hub/internal/downstream"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// LogEntry 是 call_logs 表的一行。
type LogEntry struct {
	ID          string
	TS          time.Time
	ServerID    string
	ServerKey   string
	ToolName    string
	ExposedName string
	DurationMS  int64
	Status      string // ok | error | key（密钥事件）
	ErrorKind   downstream.ErrorKind
	Error       string

	// CredentialID 归因到具体密钥，单密钥模式为空串。
	// 只记 id：日志里落一份明文凭据，等于把密钥抄到了另一个地方。
	CredentialID string

	// APIKeyID 是发起调用的接入 key（外部 AI 客户端），匿名调用为空串。
	// Args / Result 是请求现场：入参 JSON 与返回序列化，均已截断。
	// 存参数与结果是为了"复现一次调用"——排障时最想要的恰恰是
	// 当时发出去什么、拿回来什么。
	APIKeyID string
	Args     string
	Result   string
}

// AppendLog 写入一条调用日志。
func (s *Store) AppendLog(ctx context.Context, e LogEntry) error {
	if e.ID == "" {
		e.ID = NewID()
	}
	if e.TS.IsZero() {
		e.TS = time.Now()
	}

	row := logRow{
		ID:           e.ID,
		TS:           e.TS.UnixMilli(),
		ServerID:     e.ServerID,
		ServerKey:    e.ServerKey,
		ToolName:     e.ToolName,
		ExposedName:  e.ExposedName,
		DurationMS:   e.DurationMS,
		Status:       e.Status,
		ErrorKind:    string(e.ErrorKind),
		ErrorText:    e.Error,
		CredentialID: e.CredentialID,
		APIKeyID:     e.APIKeyID,
		Args:         truncate(e.Args, maxLogFieldBytes),
		Result:       truncate(e.Result, maxLogFieldBytes),
	}
	if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
		return fmt.Errorf("写入调用日志失败: %w", err)
	}
	return nil
}

// maxLogFieldBytes 是单个日志字段的落库上限。
//
// 工具返回可能是几 MB 的文档，全量落库只会让日志表膨胀到拖垮查询。
// 8 KB 足够看清"当时发生了什么"，超出部分截断丢弃——日志是复现线索，
// 不是存档。
const maxLogFieldBytes = 8 * 1024

// truncate 按 UTF-8 安全截断，超长部分追加省略标记。
func truncate(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := s[:limit]
	// 避免把多字节字符劈成半个。
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut + "…[截断]"
}

// LogQuery 是调用日志的筛选条件。零值表示"最近 100 条"。
type LogQuery struct {
	ServerID string
	Status   string // ok | error
	Kind     downstream.ErrorKind

	Since time.Time
	Until time.Time

	Limit  int
	Offset int
}

// maxLogLimit 是单次查询返回的上限。
//
// 日志表会随时间无限增长，不设上限的话一次 GET 就能把整张表
// 拉进内存——对本地界面来说那是纯粹的浪费。
const maxLogLimit = 500

// apply 把筛选条件挂到查询上。
//
// 统计与取页各调一次：GORM 的条件是累积在同一个 *gorm.DB 上的，
// 复用同一个句柄会让 WHERE 被拼两遍。
func (q LogQuery) apply(tx *gorm.DB) *gorm.DB {
	if q.ServerID != "" {
		tx = tx.Where("server_id = ?", q.ServerID)
	}
	if q.Status != "" {
		tx = tx.Where("status = ?", q.Status)
	}
	if q.Kind != "" {
		tx = tx.Where("error_kind = ?", string(q.Kind))
	}
	if !q.Since.IsZero() {
		tx = tx.Where("ts >= ?", q.Since.UnixMilli())
	}
	if !q.Until.IsZero() {
		tx = tx.Where("ts <= ?", q.Until.UnixMilli())
	}
	return tx
}

// ListLogs 返回符合条件的日志与总条数，按时间倒序。
//
// 同时返回总数是为了分页：只给当前页的条数，界面无法判断
// "还有没有下一页"，只能靠"本页不满即末页"这种会误判的猜测。
func (s *Store) ListLogs(ctx context.Context, q LogQuery) ([]LogEntry, int, error) {
	var total int64
	if err := q.apply(s.db.WithContext(ctx).Model(&logRow{})).
		Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("统计调用日志失败: %w", err)
	}

	limit := q.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > maxLogLimit {
		limit = maxLogLimit
	}
	offset := q.Offset
	if offset < 0 {
		offset = 0
	}

	var rows []logRow
	if err := q.apply(s.db.WithContext(ctx).Model(&logRow{})).
		Order(clause.OrderByColumn{Column: clause.Column{Name: "ts"}, Desc: true}).
		Limit(limit).Offset(offset).
		Find(&rows).Error; err != nil {
		return nil, 0, fmt.Errorf("查询调用日志失败: %w", err)
	}

	out := make([]LogEntry, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.toLogEntry())
	}
	return out, int(total), nil
}

// CallStats 是一段时间窗内的调用统计。
type CallStats struct {
	Total     int64   `json:"total"`
	Failed    int64   `json:"failed"`
	AvgMS     float64 `json:"avgMs"`
	MaxMS     int64   `json:"maxMs"`
	SuccessMs int64   `json:"-"`
}

// Stats 统计 since 之后的调用量与失败数。
func (s *Store) Stats(ctx context.Context, since time.Time) (CallStats, error) {
	var st CallStats
	err := s.db.WithContext(ctx).Model(&logRow{}).
		Select(`COUNT(*) AS total,
		        COALESCE(SUM(CASE WHEN status = 'error' THEN 1 ELSE 0 END), 0) AS failed,
		        COALESCE(AVG(duration_ms), 0) AS avg_ms,
		        COALESCE(MAX(duration_ms), 0) AS max_ms`).
		Where("ts >= ?", since.UnixMilli()).
		Scan(&st).Error
	if err != nil {
		return CallStats{}, fmt.Errorf("统计调用情况失败: %w", err)
	}
	return st, nil
}

// ClearLogs 清空调用日志，返回删除条数。
func (s *Store) ClearLogs(ctx context.Context) (int64, error) {
	// 全表删除必须显式放行：GORM 默认拒绝没有 WHERE 的 Delete，
	// 这条保护在别处是好事，这里要用 Session 明确表达"就是要清空"。
	res := s.db.WithContext(ctx).
		Session(&gorm.Session{AllowGlobalUpdate: true}).
		Delete(&logRow{})
	if res.Error != nil {
		return 0, fmt.Errorf("清空日志失败: %w", res.Error)
	}
	return res.RowsAffected, nil
}
