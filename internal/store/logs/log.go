// Package logs 是 call_logs 表的持久化：每次工具调用的现场记录。
package logs

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hydra/mcp-hub/internal/downstream"
	"github.com/hydra/mcp-hub/internal/store/storeutil"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// logRow 是 call_logs 表的一行（持久化形态）。
type logRow struct {
	ID           string `gorm:"column:id;primaryKey"`
	TS           int64  `gorm:"column:ts"`
	ServerID     string `gorm:"column:server_id"`
	ServerKey    string `gorm:"column:server_key"`
	ToolName     string `gorm:"column:tool_name"`
	ExposedName  string `gorm:"column:exposed_name"`
	DurationMS   int64  `gorm:"column:duration_ms"`
	Status       string `gorm:"column:status"`
	ErrorKind    string `gorm:"column:error_kind"`
	ErrorText    string `gorm:"column:error"`
	CredentialID string `gorm:"column:credential_id"`
	APIKeyID     string `gorm:"column:api_key_id"`
	APIKeyName   string `gorm:"column:api_key_name"`
	Args         string `gorm:"column:args"`
	Result       string `gorm:"column:result"`
}

func (logRow) TableName() string { return "call_logs" }

func (r logRow) toLogEntry() LogEntry {
	return LogEntry{
		ID:           r.ID,
		TS:           time.UnixMilli(r.TS),
		ServerID:     r.ServerID,
		ServerKey:    r.ServerKey,
		ToolName:     r.ToolName,
		ExposedName:  r.ExposedName,
		DurationMS:   r.DurationMS,
		Status:       r.Status,
		ErrorKind:    downstream.ErrorKind(r.ErrorKind),
		Error:        r.ErrorText,
		CredentialID: r.CredentialID,
		APIKeyID:     r.APIKeyID,
		APIKeyName:   r.APIKeyName,
		Args:         r.Args,
		Result:       r.Result,
	}
}

// LogEntry 是 call_logs 表的一行（领域形态）。
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

	// CredentialID 归因到具体密钥（单密钥模式为空串），只记 id 不记明文。
	CredentialID string

	// APIKeyID / APIKeyName 是发起调用的接入 key 及其**调用当时**的名字。
	// 名字落快照而不是查询时 join：改名或删 key 不该改写历史归因。
	//
	// Args / Result 是请求现场（入参 JSON 与返回序列化，均已截断）：
	// 排障时最想要的正是"当时发出去什么、拿回来什么"。
	APIKeyID   string
	APIKeyName string
	Args       string
	Result     string
}

// Store 是 call_logs 表的读写入口。
type Store struct {
	db *gorm.DB
}

// New 构造 logs 包的读写入口。
func New(db *gorm.DB) *Store { return &Store{db: db} }

// Append 写入一条调用日志。
func (s *Store) Append(ctx context.Context, e LogEntry) error {
	if e.ID == "" {
		e.ID = storeutil.NewID()
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
		APIKeyName:   e.APIKeyName,
		Args:         truncate(e.Args, maxLogFieldBytes),
		Result:       truncate(e.Result, maxLogFieldBytes),
	}
	if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
		return fmt.Errorf("写入调用日志失败: %w", err)
	}
	return nil
}

// maxLogFieldBytes 是单个日志字段的落库上限：工具返回可能几 MB，
// 全量落库会拖垮日志表。8KB 足够看清"当时发生了什么"。
const maxLogFieldBytes = 8 * 1024

// truncate 按 UTF-8 安全截断（不劈半个多字节字符），超长部分追加省略标记。
func truncate(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := s[:limit]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut + "…[截断]"
}

// LogSort 是可排序的列（只开放这三个：其余字段排序无可读语义）。
type LogSort string

const (
	LogSortTS       LogSort = "ts"
	LogSortDuration LogSort = "durationMs"
	LogSortStatus   LogSort = "status"
)

// LogOrder 是排序方向。零值表示倒序——日志最常看的是"最新发生了什么"。
type LogOrder string

const (
	LogOrderDesc LogOrder = "desc"
	LogOrderAsc  LogOrder = "asc"
)

// logSortColumns 是排序列白名单：请求里的值 → 真实列名。
// 列名要拼进 ORDER BY，不能把调用方的字符串直接透传。
var logSortColumns = map[LogSort]string{
	LogSortTS:       "ts",
	LogSortDuration: "duration_ms",
	LogSortStatus:   "status",
}

// LogCallerAnonymous 是 LogQuery.APIKeyID 的保留值：只看匿名调用。
// 空串在查询参数里一律表示"不筛"，所以"只要匿名"必须用非空字面量表达。
const LogCallerAnonymous = "anonymous"

// logSearchClause 是自由文本搜索的 WHERE 子句。
// 只搜"身份"字段（调用方 / Server / 工具 / 密钥），刻意不搜 8KB 的
// args / result——用户找的是"哪次调用"，不是返回体里的词。
// 列名写死在这里，没有注入面；GIN 索引等真扫不动时再加。
const logSearchClause = `(tool_name ILIKE ? ESCAPE '\' OR exposed_name ILIKE ? ESCAPE '\' OR server_key ILIKE ? ESCAPE '\' OR credential_id ILIKE ? ESCAPE '\' OR api_key_name ILIKE ? ESCAPE '\')`

// maxLogSearchRunes 是搜索词的长度上限（按 rune 截断，不劈多字节字符）。
const maxLogSearchRunes = 128

// likePattern 把用户输入转成 LIKE 模式；空输入返回空串表示不筛。
// \ % _ 三个字符必须转义，否则搜 "100%" 会筛出"以 100 开头的所有东西"。
func likePattern(q string) string {
	q = strings.TrimSpace(q)
	if q == "" {
		return ""
	}
	if r := []rune(q); len(r) > maxLogSearchRunes {
		q = string(r[:maxLogSearchRunes])
	}
	// 反斜杠放最前：Replacer 单趟扫描，\ → \\ 的产物不会被再转一遍。
	esc := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + esc.Replace(q) + "%"
}

// LogQuery 是调用日志的筛选条件。零值表示"最近 100 条，按时间倒序"。
type LogQuery struct {
	ServerID string
	Status   string // ok | error
	Kind     downstream.ErrorKind

	// Q 是自由文本搜索（见 logSearchClause）。空串或全空白表示不筛。
	Q string

	// APIKeyID 按调用方筛。传 LogCallerAnonymous 时筛匿名调用。
	APIKeyID string

	Since time.Time
	Until time.Time

	// Sort / Order 是视图偏好。非法值不报错，静默回退默认序。
	Sort  LogSort
	Order LogOrder

	Limit  int
	Offset int
}

// maxLogLimit 是单次查询返回的上限，防止一次 GET 把整张表拉进内存。
const maxLogLimit = 500

// apply 把筛选条件挂到查询上。统计与取页各调一次（GORM 条件会累积，
// 复用同一句柄会把 WHERE 拼两遍）。
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
	if pattern := likePattern(q.Q); pattern != "" {
		tx = tx.Where(logSearchClause, pattern, pattern, pattern, pattern, pattern)
	}
	if q.APIKeyID != "" {
		caller := q.APIKeyID
		if caller == LogCallerAnonymous {
			caller = ""
		}
		tx = tx.Where("api_key_id = ?", caller)
	}
	if !q.Since.IsZero() {
		tx = tx.Where("ts >= ?", q.Since.UnixMilli())
	}
	if !q.Until.IsZero() {
		tx = tx.Where("ts <= ?", q.Until.UnixMilli())
	}
	return tx
}

// order 把排序条件挂到查询上：列名过白名单；补 ts 作次级键、id 作末级键，
// 保证取值重复 / 同毫秒落库时分页不漏行不重复。
func (q LogQuery) order() []clause.OrderByColumn {
	col, ok := logSortColumns[q.Sort]
	if !ok {
		col = logSortColumns[LogSortTS]
	}

	cols := []clause.OrderByColumn{{
		Column: clause.Column{Name: col},
		// 只有 "asc"（不分大小写）算升序，写错的值回落到默认降序。
		Desc: !strings.EqualFold(string(q.Order), string(LogOrderAsc)),
	}}
	if col != "ts" {
		cols = append(cols, clause.OrderByColumn{Column: clause.Column{Name: "ts"}, Desc: true})
	}
	cols = append(cols, clause.OrderByColumn{Column: clause.Column{Name: "id"}, Desc: false})
	return cols
}

// List 返回符合条件的日志与总条数（返回总数供分页判断"还有没有下一页"）。
func (s *Store) List(ctx context.Context, q LogQuery) ([]LogEntry, int, error) {
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

	// 排序必须在 SQL 层做完再取页，否则翻页顺序会变。
	tx := q.apply(s.db.WithContext(ctx).Model(&logRow{}))
	for _, c := range q.order() {
		tx = tx.Order(c)
	}

	var rows []logRow
	if err := tx.Limit(limit).Offset(offset).Find(&rows).Error; err != nil {
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

// Clear 清空调用日志，返回删除条数。
func (s *Store) Clear(ctx context.Context) (int64, error) {
	// GORM 默认拒绝无 WHERE 的 Delete，这里用 Session 显式表达"就是要清空"。
	res := s.db.WithContext(ctx).
		Session(&gorm.Session{AllowGlobalUpdate: true}).
		Delete(&logRow{})
	if res.Error != nil {
		return 0, fmt.Errorf("清空日志失败: %w", res.Error)
	}
	return res.RowsAffected, nil
}
