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

	// 纯 Go 的 SQLite 实现（transpiled C）。选它的唯一理由是
	// CGO_ENABLED=0 就能交叉编译出三平台产物——换成 mattn 系
	// （CGO）就等于把桌面端的打包麻烦原样请回来。
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

// Normalize 把用户给的路径落成"绝对路径 + 已建好父目录"。
//
// 相对路径必须在启动时定下来：进程中途 chdir 或换工作目录，
// 相对路径会指向另一个文件，而这个错误的表现是"数据不见了"。
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
//
// 用官方 gorm sqlite dialector（DriverName 指向 modernc）而不是第三方
// 纯 Go 封装：官方这个的 DDL/迁移器是完整的，只把驱动名换掉即可；
// 第三方封装会把 modernc 再包一层，反而多一个需要跟版本的依赖。
func (*sqliteDialect) Open(dsn string) gorm.Dialector {
	return sqliteDialector{gormsqlite.Dialector{
		DriverName: sqliteDriverName,
		DSN:        sqliteDSN(dsn),
	}}
}

// sqliteDSN 把路径包成带 PRAGMA 与事务模式的连接串。
//
// 五个参数都不是可选项：
//   - foreign_keys：SQLite 默认**不**强制外键，级联删除会静默失效；
//   - journal_mode(WAL)：读写不互斥，否则一次写会挡住所有读；
//   - busy_timeout：抢锁时等待而不是立刻报 SQLITE_BUSY；
//   - synchronous(NORMAL)：WAL 下的推荐档，兼顾 Durability 与速度；
//   - _txlock=immediate：见下，这一条是并发写不丢结果的**必要条件**。
//
// _txlock=immediate 解决的是一个很隐蔽的问题：SQLite 默认的
// BEGIN 是 deferred —— 事务先以读身份开始，等到第一条写语句才尝试
// 升级成写事务。若这期间另一个连接已经写过，升级会直接失败并返回
// SQLITE_BUSY_SNAPSHOT，而且**不会**走 busy_timeout 的等待逻辑
// （因为读快照已经过期，等下去也救不回来）。
//
// 引擎里"并发探活 → 各自落库"正是这个形状：SaveProbeReported 用
// GORM 的 Transaction 先 SELECT 再 UPDATE。默认行为下，8 个上游同时
// 探活就会有若干个落库失败，而这些工具的启用状态与探活结果会静默丢失
// （错误只进日志）。改成 BEGIN IMMEDIATE 后，写锁在事务开始时就拿到，
// 抢不到就按 busy_timeout 排队——失败变成等待。
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

// tunePool 配置连接池。SQLite 的写是串行文件锁，池子开大只是把
// 争抢从进程内挪到锁上；4 个连接足够让读并发、写排队。
func (*sqliteDialect) TunePool(db *sql.DB) {
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	db.SetConnMaxIdleTime(0)
}

// AfterOpen 没有额外初始化：PRAGMA 已经在 DSN 上，每个新连接自动生效。
func (*sqliteDialect) AfterOpen(context.Context, *sql.DB) error { return nil }

// Migrate 复用已经开好的连接池（WithInstance），不走 DSN 解析。
//
// 原因是跨平台：golang-migrate 的 sqlite 驱动靠字符串替换
// "sqlite://" 前缀来取路径，Windows 的 C:\... 会被 net/url 当成
// host:port 解析而失败。WithInstance 完全绕开这条路径。
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

// LikeOp：SQLite 的 LIKE 对 ASCII 默认就不区分大小写（大小写折叠是
// 内建行为），中文本就是精确匹配，所以直接用 LIKE。
func (*sqliteDialect) LikeOp() string { return "LIKE" }

// NativeVectorSearch：SQLite 没有原生向量算子。
// 加 sqlite-vec 之类的 C 扩展会把"纯 Go 交叉编译"这个唯一的
// 选型理由抵消掉，所以向量一路固定走 Go 内存计算。
func (*sqliteDialect) NativeVectorSearch() bool { return false }

// sqliteDialector 在官方 dialector 外面套一层，只改一件事：错误翻译。
//
// 官方实现靠 json.Marshal(err) 猜 sqlite 的扩展错误码，而这要求错误
// 类型把码暴露成导出字段——modernc 的 *sq.Error 两个字段都是私有的，
// 于是 Translate 永远翻译不出来，唯一冲突会变成 500 而不是 409。
// 这里直接取 Code()，是同一件事的可靠做法。
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
