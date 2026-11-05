package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/golang-migrate/migrate/v4"
	migratesqlite "github.com/golang-migrate/migrate/v4/database/sqlite"
	"github.com/golang-migrate/migrate/v4/source/iofs"

	// 纯 Go 的 SQLite 实现（transpiled C）。选它是为了 CGO_ENABLED=0
	// 也能交叉编译出三平台产物；mattn 系（CGO）会带来桌面端打包麻烦。
	gormsqlite "gorm.io/driver/sqlite"
	"gorm.io/gorm"
	sq "modernc.org/sqlite"
)

// sqliteDriverName 是 modernc 注册到 database/sql 的驱动名。
// gorm 的 sqlite dialector 默认用 "sqlite3"（mattn），这里必须显式改掉。
const sqliteDriverName = "sqlite"

// sqliteDialect 是单文件 SQLite 后端。
type sqliteDialect struct{}

func (*sqliteDialect) Name() string { return DriverSQLite }

// Normalize 把用户给的路径落成绝对路径并建好父目录。
// 相对路径必须在启动时定死：进程中途换工作目录会指向另一个文件，
// 表现为"数据不见了"。
func (*sqliteDialect) Normalize(dsn string) (string, error) {
	path := strings.TrimSpace(dsn)
	if path == "" {
		return "", errors.New("SQLite 需要数据库文件路径")
	}
	// 容忍误写成 DSN 形态的值（file:... 是 modernc 认的形态，原样放行）。
	if !strings.HasPrefix(path, "file:") {
		abs, err := filepath.Abs(path)
		if err != nil {
			return "", fmt.Errorf("解析 SQLite 路径 %q 失败: %w", path, err)
		}
		path = abs
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return "", fmt.Errorf("创建 SQLite 目录 %s 失败: %w", filepath.Dir(path), err)
		}
	}
	return path, nil
}

// Open 返回 gorm dialector。
// 用官方 gorm sqlite dialector（DriverName 指向 modernc），只换驱动名，
// 其 DDL/迁移器完整，也少一个需要跟版本的依赖。
func (*sqliteDialect) Open(dsn string) gorm.Dialector {
	return sqliteDialector{gormsqlite.Dialector{
		DriverName: sqliteDriverName,
		DSN:        sqliteDSN(dsn),
	}}
}

// sqliteDSN 把路径包成带 PRAGMA 与事务模式的连接串。五个参数都不是可选项：foreign_keys 默认关闭必须显式开；
// WAL 让读写不互斥，busy_timeout 让抢锁等待而非立即报 SQLITE_BUSY，synchronous(NORMAL) 是 WAL 推荐档；
// _txlock=immediate 在 BEGIN 时取得写锁，避免 deferred 升级失败（SQLITE_BUSY_SNAPSHOT 且不走 busy_timeout）导致并发落库静默丢失。
func sqliteDSN(path string) string {
	if strings.HasPrefix(path, "file:") {
		return path
	}
	q := url.Values{}
	for _, p := range []string{
		"foreign_keys(1)",
		"journal_mode(WAL)",
		"busy_timeout(5000)",
		"synchronous(NORMAL)",
	} {
		q.Add("_pragma", p)
	}
	q.Set("_txlock", "immediate")
	return "file:" + filepath.ToSlash(path) + "?" + q.Encode()
}

// tunePool 配置连接池。SQLite 写是串行文件锁，池子开大只是把争抢挪到锁上；
// 4 个连接足够让读并发、写排队。
func (*sqliteDialect) TunePool(db *sql.DB) {
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	db.SetConnMaxIdleTime(0)
}

// AfterOpen 没有额外初始化：PRAGMA 已经在 DSN 上，每个新连接自动生效。
func (*sqliteDialect) AfterOpen(context.Context, *sql.DB) error { return nil }

// Migrate 复用已经开好的连接池（WithInstance）。golang-migrate 的 sqlite
// 驱动靠字符串替换前缀取路径，Windows 的 C:\... 会被 net/url 当成 host:port 而失败。
func (*sqliteDialect) Migrate(db *sql.DB, _ string) error {
	src, err := iofs.New(migrations, "migrations/"+DriverSQLite)
	if err != nil {
		return fmt.Errorf("读取内嵌迁移文件失败: %w", err)
	}
	drv, err := migratesqlite.WithInstance(db, &migratesqlite.Config{})
	if err != nil {
		return fmt.Errorf("初始化迁移驱动失败: %w", err)
	}
	m, err := migrate.NewWithInstance("iofs", src, DriverSQLite, drv)
	if err != nil {
		return fmt.Errorf("初始化迁移失败: %w", err)
	}
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("执行数据库迁移失败: %w", err)
	}
	return nil
}

// LikeOp：SQLite 的 LIKE 对 ASCII 默认就不区分大小写，中文本就是精确匹配。
func (*sqliteDialect) LikeOp() string { return "LIKE" }

// NativeVectorSearch：SQLite 没有原生向量算子。sqlite-vec 之类的 C 扩展
// 会抵消纯 Go 交叉编译的选型理由，向量固定走 Go 内存计算。
func (*sqliteDialect) NativeVectorSearch() bool { return false }

// sqliteDialector 套在官方 dialector 外，只改错误翻译。
// 官方靠 json.Marshal(err) 猜扩展错误码，modernc 的 *sq.Error 字段是私有的，
// 冲突会变成 500 而不是 409；这里直接取 Code()。
type sqliteDialector struct {
	gormsqlite.Dialector
}

// SQLite 扩展结果码（https://www.sqlite.org/rescode.html）。
const (
	sqliteConstraintPrimaryKey = 1555
	sqliteConstraintUnique     = 2067
	sqliteConstraintRowID      = 2579
	sqliteConstraintForeignKey = 787
	sqliteConstraintFkNotNull  = 1811
)

func (d sqliteDialector) Translate(err error) error {
	var se *sq.Error
	if errors.As(err, &se) {
		switch se.Code() {
		case sqliteConstraintPrimaryKey, sqliteConstraintUnique, sqliteConstraintRowID:
			return gorm.ErrDuplicatedKey
		case sqliteConstraintForeignKey, sqliteConstraintFkNotNull:
			return gorm.ErrForeignKeyViolated
		}
	}
	return d.Dialector.Translate(err)
}
