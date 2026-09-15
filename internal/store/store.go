package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	gormlogger "gorm.io/gorm/logger"
)

//go:embed schema.sql
var schemaSQL string

// schemaVersion 与 schema.sql 对应，记录在 schema_meta 表里。
//
// v1 → v2：新增 credentials 表；servers 加 est_tokens_per_call；
// call_logs 加 credential_id。
// v2 → v3：servers 加元数据列（description / tags / instructions /
// category_id）；新增 categories / tool_embeddings / app_config 三张表。
// v3 → v4：servers 加 auth_mode（鉴权形态标识位，空=按 headers 推断）。
// v4 → v5：call_logs 加 api_key_id / args / result；新增 api_keys 表
// （接入 key 只存 sha256，/mcp 端点鉴权用）。
const schemaVersion = 5

// schemaMetaID 是 schema_meta 表里唯一那一行的主键。
const schemaMetaID = 1

// ErrNotFound 表示目标记录不存在。
var ErrNotFound = errors.New("record not found")

// ErrKeyTaken 表示工具前缀已被另一个 server 占用。
//
// 单独成一种错误，好让上层把它映射成 409 而不是笼统的 500——
// key 撞车是用户的输入问题，不是服务故障。
var ErrKeyTaken = errors.New("server key already in use")

// ErrCategoryTaken 表示分类名已被占用。
var ErrCategoryTaken = errors.New("category name already in use")

// Dialect 是底层数据库类型。
type Dialect string

const (
	// DialectPostgres 是生产形态。
	DialectPostgres Dialect = "postgres"
	// DialectSQLite 是本地开发与测试形态，后期会被 PG 替换掉。
	DialectSQLite Dialect = "sqlite"
)

// ToolFilterMode 决定 server 的工具如何进入聚合池。
type ToolFilterMode string

const (
	// FilterAllowAll 不做过滤，下游工具全部进入聚合池。
	FilterAllowAll ToolFilterMode = "allow_all"
	// FilterAllow 白名单：只放行名单内的工具。
	FilterAllow ToolFilterMode = "allow"
	// FilterBlock 黑名单：排除名单内的工具。
	FilterBlock ToolFilterMode = "block"
)

// Valid 报告过滤模式是否受支持。
func (m ToolFilterMode) Valid() bool {
	switch m {
	case FilterAllowAll, FilterAllow, FilterBlock:
		return true
	}
	return false
}

// Normalize 把空值折算成默认模式。
func (m ToolFilterMode) Normalize() ToolFilterMode {
	if m == "" {
		return FilterAllowAll
	}
	return m
}

// Validate 检查模式与名单是否自洽。
//
// allow 模式配空名单会静默清空该 server 的全部工具——这是配置到一半
// 最容易踩的坑，且症状是"AI 的工具凭空消失"，极难定位。因此直接在
// 入口拒绝，而不是让它生效后在调用路径上暴露。
func (m ToolFilterMode) Validate(list []string) error {
	if !m.Normalize().Valid() {
		return fmt.Errorf("未知的工具过滤模式 %q", m)
	}
	if m.Normalize() != FilterAllowAll && len(CleanToolNames(list)) == 0 {
		return fmt.Errorf("%s 模式需要提供非空的工具名单", m.Normalize())
	}
	return nil
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

// Options 是打开数据库的参数。
type Options struct {
	// DSN 是 postgres://… 连接串，或 SQLite 的文件路径。
	DSN string
	// Dialect 留空时按 DSN 形态推断。
	Dialect Dialect
	// Log 留空则关闭 GORM 自身的 SQL 日志。
	Log *slog.Logger
}

// Store 是持久层，全部 SQL 由 GORM 生成。
//
// 支持 PG 与 SQLite 两种方言，模型与查询写成方言无关的形式；
// 方言差异只收敛在 OpenWith 与两处索引声明里。
type Store struct {
	db   *gorm.DB
	kind Dialect
}

// Open 打开（必要时创建）数据库并同步 schema。
//
// DSN 形态决定方言：postgres:// 或 postgresql:// 走 PG，
// 其余一律当作 SQLite 文件路径。
func Open(dsn string) (*Store, error) { return OpenWith(Options{DSN: dsn}) }

// OpenWith 打开数据库并同步 schema。
func OpenWith(opts Options) (*Store, error) {
	kind := opts.Dialect
	if kind == "" {
		kind = ResolveDialect(opts.DSN)
	}

	var dialector gorm.Dialector
	switch kind {
	case DialectPostgres:
		dialector = postgres.Open(opts.DSN)
	case DialectSQLite:
		dialector = sqlite.Open(sqliteDSN(opts.DSN))
	default:
		return nil, fmt.Errorf("不支持的数据库类型 %q", kind)
	}

	db, err := gorm.Open(dialector, &gorm.Config{
		Logger: newLogger(opts.Log),
		// 让驱动把唯一约束/外键冲突翻译成 gorm.ErrDuplicatedKey /
		// gorm.ErrForeignKeyViolated，两条方言的判定就能统一。
		TranslateError: true,
		// 单条 INSERT 的行数上限。工具有可能上千条，一次性拼出来的
		// 语句会撞上驱动的绑定参数上限。
		CreateBatchSize: 200,
	})
	if err != nil {
		return nil, fmt.Errorf("打开数据库失败: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("获取连接池失败: %w", err)
	}
	tunePool(sqlDB, kind)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := sqlDB.PingContext(ctx); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("连接数据库失败: %w", err)
	}

	s := &Store{db: db, kind: kind}
	if err := s.migrate(ctx); err != nil {
		sqlDB.Close()
		return nil, err
	}
	return s, nil
}

// ResolveDialect 依据 DSN 形态判断数据库类型。
func ResolveDialect(dsn string) Dialect {
	lower := strings.ToLower(strings.TrimSpace(dsn))
	if strings.HasPrefix(lower, "postgres://") || strings.HasPrefix(lower, "postgresql://") {
		return DialectPostgres
	}
	return DialectSQLite
}

// sqliteDSN 补上 WAL / 忙等 / 外键三个 pragma。
//
// 外键不是可选项：工具缓存与密钥都靠 ON DELETE CASCADE 随 server 一起
// 清理，关掉它会让删除 server 之后留下一堆够不着的孤儿行。
func sqliteDSN(path string) string {
	if strings.HasPrefix(path, "file:") {
		return path
	}
	return fmt.Sprintf(
		"file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)",
		path,
	)
}

// tunePool 按方言配置连接池。
func tunePool(sqlDB *sql.DB, kind Dialect) {
	switch kind {
	case DialectSQLite:
		// 写路径串行化。写入量极低（配置变更、探活结果、调用日志），
		// 换来的是彻底规避 SQLite 的写锁竞争。
		sqlDB.SetMaxOpenConns(1)
	default:
		// PG 的连接是进程外的资源，池子开太大只是把压力转移给数据库。
		sqlDB.SetMaxOpenConns(16)
		sqlDB.SetMaxIdleConns(8)
		sqlDB.SetConnMaxIdleTime(5 * time.Minute)
	}
}

// migrate 应用 DDL 基线，再补齐增量。
//
// DDL 由 schema.sql 独占，GORM 不参与建表。理由见 schema.sql 抬头：
// AutoMigrate 在 SQLite 上要靠文本解析已有表的 DDL 判断差异，
// 解析错列清单就会触发一次 DROP TABLE + RENAME 的"修复"。
func (s *Store) migrate(ctx context.Context) error {
	// 基线语句全是 IF NOT EXISTS，对已存在的库是空操作，因此每次都执行。
	for _, stmt := range splitStatements(schemaSQL) {
		if err := s.db.WithContext(ctx).Exec(stmt).Error; err != nil {
			return fmt.Errorf("应用 schema 失败: %w", err)
		}
	}

	version, err := s.readSchemaVersion(ctx)
	if err != nil {
		return err
	}

	// 老库的增量部分必须显式处理：CREATE TABLE IF NOT EXISTS 见到
	// 已存在的表就直接跳过，不会给它补上新增的列。
	if version < 2 {
		for _, col := range []columnSpec{
			{"servers", "est_tokens_per_call", "INTEGER NOT NULL DEFAULT 0"},
			{"call_logs", "credential_id", "TEXT NOT NULL DEFAULT ''"},
		} {
			if err := s.addColumnIfMissing(ctx, col); err != nil {
				return err
			}
		}
	}

	if version < 3 {
		for _, col := range []columnSpec{
			{"servers", "description", "TEXT NOT NULL DEFAULT ''"},
			{"servers", "tags", "TEXT NOT NULL DEFAULT '[]'"},
			{"servers", "instructions", "TEXT NOT NULL DEFAULT ''"},
			{"servers", "category_id", "TEXT NOT NULL DEFAULT ''"},
		} {
			if err := s.addColumnIfMissing(ctx, col); err != nil {
				return err
			}
		}
		// PG 上启用 pgvector（best effort：没装扩展也能启动，
		// hybrid 检索按降级阶梯退回，见 PRD §4.7.4）。
		if s.kind == DialectPostgres {
			if err := s.db.WithContext(ctx).Exec("CREATE EXTENSION IF NOT EXISTS vector").Error; err != nil {
				log.Printf("store: pgvector 扩展不可用，hybrid 检索将降级: %v", err)
			}
		}
	}

	if version < 4 {
		// 鉴权形态标识位。老库的空值按 AuthModeOr 的推断规则解释，
		// 不做数据回填——推断与回填的差别用户永远看不到。
		if err := s.addColumnIfMissing(ctx, columnSpec{
			"servers", "auth_mode", "TEXT NOT NULL DEFAULT ''",
		}); err != nil {
			return err
		}
	}

	if version < 5 {
		// api_keys 表由基线建全；老库只需给 call_logs 补列。
		for _, col := range []columnSpec{
			{"call_logs", "api_key_id", "TEXT NOT NULL DEFAULT ''"},
			{"call_logs", "args", "TEXT NOT NULL DEFAULT ''"},
			{"call_logs", "result", "TEXT NOT NULL DEFAULT ''"},
		} {
			if err := s.addColumnIfMissing(ctx, col); err != nil {
				return err
			}
		}
	}

	return s.writeSchemaVersion(ctx, schemaVersion)
}

// columnSpec 描述一次"给旧表补列"。
type columnSpec struct{ Table, Name, Decl string }

// readSchemaVersion 读当前 schema 版本，没有记录时返回 0。
//
// 从 PRAGMA user_version 换成了一张表：后者只有 SQLite 有，
// 而 PG 侧没有等价的、能跨两种方言的轻量机制。
// 老库升级上来时这里读到 0，会把幂等的增量步骤再跑一遍，无损。
func (s *Store) readSchemaVersion(ctx context.Context) (int, error) {
	var versions []int64
	err := s.db.WithContext(ctx).Table("schema_meta").
		Where("id = ?", schemaMetaID).Limit(1).Pluck("version", &versions).Error
	if err != nil {
		return 0, fmt.Errorf("读取 schema 版本失败: %w", err)
	}
	if len(versions) == 0 {
		return 0, nil
	}
	return int(versions[0]), nil
}

func (s *Store) writeSchemaVersion(ctx context.Context, version int) error {
	err := s.db.WithContext(ctx).Table("schema_meta").
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "id"}},
			DoUpdates: clause.AssignmentColumns([]string{"version"}),
		}).
		Create(map[string]any{"id": schemaMetaID, "version": version}).Error
	if err != nil {
		return fmt.Errorf("写入 schema 版本失败: %w", err)
	}
	return nil
}

// addColumnIfMissing 给已存在的表补一列。
//
// 用 `SELECT * ... LIMIT 0` 读列名，而不是查 PRAGMA table_info 或
// information_schema：一条语句同时覆盖 PG 与 SQLite，省掉一套
// 方言分支。ADD COLUMN 没有 IF NOT EXISTS 的通用形式，所以必须先查。
func (s *Store) addColumnIfMissing(ctx context.Context, col columnSpec) error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return fmt.Errorf("获取连接池失败: %w", err)
	}

	rows, err := sqlDB.QueryContext(ctx, fmt.Sprintf("SELECT * FROM %s LIMIT 0", col.Table))
	if err != nil {
		return fmt.Errorf("读取表结构失败(%s): %w", col.Table, err)
	}
	cols, err := rows.Columns()
	rows.Close()
	if err != nil {
		return fmt.Errorf("读取表结构失败(%s): %w", col.Table, err)
	}
	for _, name := range cols {
		if strings.EqualFold(name, col.Name) {
			return nil
		}
	}

	err = s.db.WithContext(ctx).
		Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", col.Table, col.Name, col.Decl)).Error
	if err != nil {
		return fmt.Errorf("为 %s 添加列 %s 失败: %w", col.Table, col.Name, err)
	}
	return nil
}

// splitStatements 把 DDL 脚本切成单条语句逐条执行。
//
// 不能整段丢给驱动：PG 的 pgx 走扩展协议时一次只接受一条语句，
// 而 SQLite 驱动对多语句的行为没有保证。
//
// 剥注释用的是"行内 -- 之后全丢掉"。对本文件是安全的——DDL 里没有
// 含 -- 的字符串字面量；换一个脚本就得换成正经的分词器。
func splitStatements(script string) []string {
	var buf strings.Builder
	for _, line := range strings.Split(script, "\n") {
		if idx := strings.Index(line, "--"); idx >= 0 {
			line = line[:idx]
		}
		buf.WriteString(line)
		buf.WriteByte('\n')
	}

	var out []string
	for _, stmt := range strings.Split(buf.String(), ";") {
		if trimmed := strings.TrimSpace(stmt); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// Close 关闭数据库。
func (s *Store) Close() error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

// GORM 暴露底层句柄，供需要拼原生查询的调用方使用。
func (s *Store) GORM() *gorm.DB { return s.db }

// Kind 返回当前方言。
func (s *Store) Kind() Dialect { return s.kind }

// NewID 生成一个 16 字节的随机标识。
func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("crypto/rand 不可用: %v", err))
	}
	return hex.EncodeToString(b[:])
}

// --- 内部辅助 ---

// isUniqueViolation 判断错误是否来自唯一约束冲突。
//
// 常规路径上 key / label 撞车已经被写入前的预检挡住了，这里兜的是
// 并发写入同一个 key 的竞态窗口——那一条错误也该被翻译成 409，
// 而不是 500。两种方言的驱动都把唯一冲突翻译成 gorm.ErrDuplicatedKey。
func isUniqueViolation(err error) bool {
	return err != nil && errors.Is(err, gorm.ErrDuplicatedKey)
}

// requireAffected 把"0 行受影响"折算成 ErrNotFound。
func requireAffected(rows int64, err error, what string) error {
	if err != nil {
		return fmt.Errorf("%s失败: %w", what, err)
	}
	if rows == 0 {
		return ErrNotFound
	}
	return nil
}

func boolToInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

func intToBool(v int64) bool { return v != 0 }

func jsonOrEmptyMap(m map[string]string) string {
	if len(m) == 0 {
		return "{}"
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func jsonOrEmptySlice(v any) string {
	b, err := json.Marshal(v)
	if err != nil || string(b) == "null" {
		return "[]"
	}
	return string(b)
}

func decodeStringMap(raw string) map[string]string {
	if raw == "" {
		return nil
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil
	}
	return m
}

func decodeStringSlice(raw string) []string {
	if raw == "" {
		return nil
	}
	var v []string
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return nil
	}
	return v
}

// newLogger 把 GORM 的 SQL 日志接到 slog 上。
//
// 不传 logger 就整条关掉：调用路径上的每次查询都打一行日志，
// 会把有用的那几行淹掉，而错误本身都已经通过返回值传出去了。
func newLogger(log *slog.Logger) gormlogger.Interface {
	if log == nil {
		return gormlogger.Discard
	}
	return gormlogger.New(slogPrinter{log}, gormlogger.Config{
		SlowThreshold:             200 * time.Millisecond,
		LogLevel:                  gormlogger.Warn,
		IgnoreRecordNotFoundError: true,
		Colorful:                  false,
	})
}

type slogPrinter struct{ log *slog.Logger }

func (p slogPrinter) Printf(format string, args ...any) {
	p.log.Debug(strings.TrimSuffix(fmt.Sprintf(format, args...), "\n"))
}
