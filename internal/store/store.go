// Package store 是 mcp-hub 的持久层门面：打开数据库、执行迁移，
// 并把各领域子包聚合成一个 Store。
//
// 每张表（组）的读写逻辑在各自的子包里——servers、credentials、apikeys、
// logs、categories、embeddings，共享的哨兵错误与行映射辅助在 storeutil。
// 本包只做三件事：连接管理、schema 迁移、装配。
package store

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"log/slog"
	"strings"
	"time"

	"github.com/hydra/mcp-hub/internal/store/apikeys"
	"github.com/hydra/mcp-hub/internal/store/categories"
	"github.com/hydra/mcp-hub/internal/store/credentials"
	"github.com/hydra/mcp-hub/internal/store/embeddings"
	"github.com/hydra/mcp-hub/internal/store/logs"
	"github.com/hydra/mcp-hub/internal/store/servers"
	"github.com/hydra/mcp-hub/internal/store/toolchanges"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// Options 是打开数据库的参数。
type Options struct {
	// DSN 是 postgres://… 连接串。
	DSN string
	// Log 留空则关闭 GORM 自身的 SQL 日志。
	Log *slog.Logger
}

// Store 是持久层入口：一个连接池 + 各领域的子 Store。
// 上层按领域走对应字段，如 st.Servers.Upsert(...)、st.Credentials.List(...)。
type Store struct {
	db *gorm.DB

	Servers     *servers.Store
	Credentials *credentials.Store
	APIKeys     *apikeys.Store
	Logs        *logs.Store
	Categories  *categories.Store
	Embeddings  *embeddings.Store
	ToolChanges *toolchanges.Store
}

// Open 打开（必要时创建）数据库并同步 schema。DSN 必须指向 PostgreSQL。
func Open(dsn string) (*Store, error) { return OpenWith(Options{DSN: dsn}) }

// OpenWith 打开数据库并同步 schema。
func OpenWith(opts Options) (*Store, error) {
	lower := strings.ToLower(strings.TrimSpace(opts.DSN))
	if !strings.HasPrefix(lower, "postgres://") && !strings.HasPrefix(lower, "postgresql://") {
		return nil, fmt.Errorf("DSN 必须是 postgres:// 连接串，收到 %q", opts.DSN)
	}

	db, err := gorm.Open(postgres.Open(opts.DSN), &gorm.Config{
		Logger: newLogger(opts.Log),
		// 把唯一约束/外键冲突翻译成 gorm.ErrDuplicatedKey / gorm.ErrForeignKeyViolated。
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
	tunePool(sqlDB)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := sqlDB.PingContext(ctx); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("连接数据库失败: %w", err)
	}

	// pgvector 可选：装不上也能启动，hybrid 检索按降级阶梯退回。
	if _, err := sqlDB.ExecContext(ctx, "CREATE EXTENSION IF NOT EXISTS vector"); err != nil {
		log.Printf("store: pgvector 扩展不可用，hybrid 检索将降级: %v", err)
	}

	if err := runMigrations(opts.DSN); err != nil {
		sqlDB.Close()
		return nil, err
	}

	return &Store{
		db:          db,
		Servers:     servers.New(db),
		Credentials: credentials.New(db),
		APIKeys:     apikeys.New(db),
		Logs:        logs.New(db),
		Categories:  categories.New(db),
		Embeddings:  embeddings.New(db),
		ToolChanges: toolchanges.New(db),
	}, nil
}

// tunePool 配置连接池。PG 连接是进程外资源，池子开太大只是把压力转嫁给数据库。
func tunePool(sqlDB *sql.DB) {
	sqlDB.SetMaxOpenConns(16)
	sqlDB.SetMaxIdleConns(8)
	sqlDB.SetConnMaxIdleTime(5 * time.Minute)
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
