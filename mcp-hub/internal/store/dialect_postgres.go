package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// postgresDialect 是 PostgreSQL 后端。
//
// 唯一的状态是 nativeVector：pgvector 装没装上只有连上之后才知道，
// 而这个判断要影响 embeddings 子 Store 的构造，所以探测结果留在实例上。
// 每次 Open 都会 new 一个，不存在跨连接串串味的问题。
type postgresDialect struct {
	nativeVector bool
}

func (*postgresDialect) Name() string { return DriverPostgres }

// Normalize 只做形态校验：DSN 的其余部分（sslmode、search_path 等）
// 是调用方的事，不该在这里被改写。
func (*postgresDialect) Normalize(dsn string) (string, error) {
	if !isPostgresDSN(dsn) {
		return "", fmt.Errorf("DSN 必须是 postgres:// 连接串，收到 %q", dsn)
	}
	return dsn, nil
}

func (*postgresDialect) Open(dsn string) gorm.Dialector { return postgres.Open(dsn) }

// TunePool 配置连接池。PG 连接是进程外资源，池子开太大只是把压力转嫁给数据库。
func (*postgresDialect) TunePool(db *sql.DB) {
	db.SetMaxOpenConns(16)
	db.SetMaxIdleConns(8)
	db.SetConnMaxIdleTime(5 * time.Minute)
}

// AfterOpen 确保 pgvector 可用并记录探测结果。
//
// 000007 起 vector 列已是 pgvector 的 vector 类型，扩展是 PostgreSQL
// 部署的硬依赖。这里仍先 CREATE EXTENSION 并只记日志不返回错误：
// 缺扩展时真正的失败发生在迁移阶段，让迁移报出准确的位置。
func (d *postgresDialect) AfterOpen(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, "CREATE EXTENSION IF NOT EXISTS vector"); err != nil {
		log.Printf("store: pgvector 扩展不可用，迁移 000007 大概率会失败: %v", err)
		return nil
	}
	var ok bool
	if err := db.QueryRowContext(ctx,
		"SELECT EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'vector')").Scan(&ok); err != nil {
		log.Printf("store: 查询 pgvector 状态失败，按不可用处理: %v", err)
		return nil
	}
	d.nativeVector = ok
	if !ok {
		log.Printf("store: pgvector 未安装，向量检索退回 Go 侧内存计算（PostgreSQL 部署应安装 pgvector，迁移 000007 依赖它）")
	}
	return nil
}

// Migrate 用 DSN 形态驱动 golang-migrate：postgres 驱动自带 advisory lock，
// 迁移期间的并发启动会被串行化，这正是我们要的。
func (*postgresDialect) Migrate(_ *sql.DB, dsn string) error {
	src, err := iofs.New(migrations, "migrations/"+DriverPostgres)
	if err != nil {
		return fmt.Errorf("读取内嵌迁移文件失败: %w", err)
	}
	m, err := migrate.NewWithSourceInstance("iofs", src, dsn)
	if err != nil {
		return fmt.Errorf("初始化迁移失败: %w", err)
	}
	defer m.Close()
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("执行数据库迁移失败: %w", err)
	}
	return nil
}

func (*postgresDialect) LikeOp() string { return "ILIKE" }

// NativeVectorSearch 由 AfterOpen 的探测结果决定。
// 未探测（例如单测直接构造 Dialect）时保守返回 false：
// 退回内存计算只是慢一点，用错算子却是一条必然失败的查询。
func (d *postgresDialect) NativeVectorSearch() bool { return d.nativeVector }
