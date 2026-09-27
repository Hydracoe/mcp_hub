// Package store 是 mcp-hub 的持久层门面：打开数据库、执行迁移，
// 并把各领域子包聚合成一个 Store。
//
// 每张表（组）的读写逻辑在各自的子包里——servers、credentials、apikeys、
// logs、categories、embeddings，共享的哨兵错误与行映射辅助在 storeutil。
// 本包只做四件事：选方言、连接管理、schema 迁移、装配。
//
// 后端差异全部收在 Dialect 接口里（见 dialect.go）。除 dialect_*.go 之外，
// 本包与子包都不该出现 postgres / sqlite 这类字面量——那是配置层与装配层的事。
package store

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/hydra/mcp-hub/internal/store/apikeys"
	"github.com/hydra/mcp-hub/internal/store/categories"
	"github.com/hydra/mcp-hub/internal/store/credentials"
	"github.com/hydra/mcp-hub/internal/store/embeddings"
	"github.com/hydra/mcp-hub/internal/store/logs"
	"github.com/hydra/mcp-hub/internal/store/servers"
	"github.com/hydra/mcp-hub/internal/store/toolchanges"

	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// Options 是打开数据库的参数。
type Options struct {
	// Driver 是后端名：auto / postgres / sqlite。留空等于 auto，
	// 按 DSN 的形态推断（postgres:// 前缀即 postgres，其余即 sqlite）。
	Driver string

	// DSN 是连接串：postgres://… 或 SQLite 文件路径。
	// SQLite 下留空则用 DataDir 里的默认文件名。
	DSN string

	// DataDir 是"调用方认为数据该放哪"的目录。
	//
	// 这一项存在的唯一理由是：桌面端的数据属于应用（应落在系统的
	// Application Support 目录），命令行工具的数据属于当前工作目录。
	// store 不认识任何一种部署形态，所以由入口把它传进来。
	// 非 SQLite 后端忽略本项。
	DataDir string

	// Log 留空则关闭 GORM 自身的 SQL 日志。
	Log *slog.Logger
}

// DefaultSQLiteFileName 是 SQLite 后端的默认库文件名。
const DefaultSQLiteFileName = "mcp-hub.db"

// Store 是持久层入口：一个连接池 + 各领域的子 Store。
// 上层按领域走对应字段，如 st.Servers.Upsert(...)、st.Credentials.List(...)。
type Store struct {
	db      *gorm.DB
	dialect Dialect

	Servers     *servers.Store
	Credentials *credentials.Store
	APIKeys     *apikeys.Store
	Logs        *logs.Store
	Categories  *categories.Store
	Embeddings  *embeddings.Store
	ToolChanges *toolchanges.Store
}

// Dialect 返回实际使用的方言，供装配层记录"跑在哪个后端上"。
func (s *Store) Dialect() Dialect { return s.dialect }

// DB 暴露底层 ORM 句柄：迁移、健康检查这类"不属于任何领域"的事需要它。
func (s *Store) DB() *gorm.DB { return s.db }

// Open 打开（必要时创建）数据库并同步 schema。
// 后端按 DSN 形态推断——postgres:// 前缀即 PostgreSQL，其余按 SQLite 文件路径。
func Open(dsn string) (*Store, error) {
	return OpenWith(Options{DSN: dsn})
}

// OpenWith 打开数据库并同步 schema。这是装配层唯一需要调用的入口。
func OpenWith(opts Options) (*Store, error) {
	dialect, err := ResolveDialect(opts.Driver, opts.DSN)
	if err != nil {
		return nil, err
	}

	dsn := strings.TrimSpace(opts.DSN)
	if dsn == "" && dialect.Name() == DriverSQLite {
		// SQLite 允许零配置：没给路径就用 DataDir 下的默认文件。
		dir := strings.TrimSpace(opts.DataDir)
		if dir == "" {
			return nil, fmt.Errorf("SQLite 后端需要给出库文件路径（MCPHUB_DB），或由调用方提供数据目录")
		}
		dsn = filepath.Join(dir, DefaultSQLiteFileName)
	}
	dsn, err = dialect.Normalize(dsn)
	if err != nil {
		return nil, err
	}

	db, err := gorm.Open(dialect.Open(dsn), &gorm.Config{
		Logger: newLogger(opts.Log),
		// 把唯一约束/外键冲突翻译成 gorm.ErrDuplicatedKey / gorm.ErrForeignKeyViolated。
		// 翻译本身由各方言实现（SQLite 那层是自定义的，见 dialect_sqlite.go）。
		TranslateError: true,
		// 单条 INSERT 的行数上限：工具可能上千条，一次拼完会撞驱动的绑定参数上限。
		CreateBatchSize: 200,
	})
	if err != nil {
		return nil, fmt.Errorf("打开数据库失败: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("获取连接池失败: %w", err)
	}
	dialect.TunePool(sqlDB)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := sqlDB.PingContext(ctx); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("连接数据库失败: %w", err)
	}

	// 补充初始化只在方言内部处置：PG 探测 pgvector、SQLite 什么都不用做。
	// 它不阻断启动（内部只记日志），因为缺少向量能力只影响检索质量。
	_ = dialect.AfterOpen(ctx, sqlDB)

	if err := dialect.Migrate(sqlDB, dsn); err != nil {
		sqlDB.Close()
		return nil, err
	}

	return &Store{
		db:          db,
		dialect:     dialect,
		Servers:     servers.New(db),
		Credentials: credentials.New(db),
		APIKeys:     apikeys.New(db),
		Categories:  categories.New(db),
		ToolChanges: toolchanges.New(db),
		// 这两个子 Store 的构造需要方言信息：日志查询的 LIKE 运算符名、
		// 向量检索走 SQL 还是 Go。装配点在这里，调用方不必知道。
		Logs:       logs.New(db, logs.Options{LikeOp: dialect.LikeOp()}),
		Embeddings: embeddings.New(db, embeddings.Options{NativeVectorSearch: dialect.NativeVectorSearch()}),
	}, nil
}

// Close 关闭数据库。
func (s *Store) Close() error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

// newLogger 把 GORM 的 SQL 日志接到 slog 上；未传 logger 时整条关掉
// （错误已经通过返回值传出，每次查询都打日志只会淹没有用的行）。
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
