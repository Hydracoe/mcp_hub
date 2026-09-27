package store

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"strings"

	"gorm.io/gorm"
)

// DriverAuto 表示"按连接串形态推断后端"。
const DriverAuto = "auto"

// 支持的后端名。与 config 包的取值保持一致——刻意的字符串常量重复：
// store 是业务层，不该 import 配置层（那是装配层的事）。
const (
	DriverPostgres = "postgres"
	DriverSQLite   = "sqlite"
)

// Dialect 把"哪一种数据库"这一件事收成一个接口。
//
// 存在的意义不是"将来可以随便换库"，而是把方言差异集中到一处：
// 迁移文件、LIKE 的大小写语义、有没有原生向量算子——这三件事以前
// 散在 store.go / migrate.go / logs / embeddings 四个地方，现在只有这里。
//
// 业务层（hub / httpd / api）不认识本接口，也永远不该认识。
type Dialect interface {
	// Name 是后端名（postgres / sqlite），也是迁移文件目录名。
	Name() string

	// Normalize 校验并规范化连接串：相对路径转绝对路径、确保父目录存在。
	// 返回值是后续 Open / Migrate 共用的最终形态。
	Normalize(dsn string) (string, error)

	// Open 返回该方言的 GORM dialector。
	Open(dsn string) gorm.Dialector

	// TunePool 配置连接池。两者的资源模型完全不同：
	// PG 连接是进程外资源（池子开大只是把压力转给数据库），
	// SQLite 写是串行的（池子开大换来的是锁等待）。
	TunePool(db *sql.DB)

	// AfterOpen 做连接建立后的补充初始化（PG 建扩展、SQLite 设 PRAGMA）。
	// 返回值只是提示，不该阻断启动。
	AfterOpen(ctx context.Context, db *sql.DB) error

	// Migrate 把库升级到最新 schema。
	Migrate(db *sql.DB, dsn string) error

	// LikeOp 返回"大小写不敏感匹配"的运算符名：PG 是 ILIKE，SQLite 是 LIKE。
	LikeOp() string

	// NativeVectorSearch 表示 SQL 侧是否有原生向量距离算子（pgvector）。
	// false 时向量检索退回 Go 侧内存计算，结果等价。
	NativeVectorSearch() bool
}

// dialects 是后端名到实现的注册表。加一个后端只动这张表。
//
// 值都是指针：PG 方言要记住"pgvector 装没装上"这个探测结果，
// 而每次 Resolve 都新建一个实例，探测结果不会跨连接串串味。
var dialects = map[string]func() Dialect{
	DriverPostgres: func() Dialect { return &postgresDialect{} },
	DriverSQLite:   func() Dialect { return &sqliteDialect{} },
}

// DialectByName 按后端名取实现。
func DialectByName(name string) (Dialect, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", DriverAuto:
		return nil, fmt.Errorf("后端名不能是空或 %q：auto 要先推断成具体后端", DriverAuto)
	}
	newDialect, ok := dialects[strings.ToLower(strings.TrimSpace(name))]
	if !ok {
		return nil, fmt.Errorf("不支持的数据库后端 %q，可选 %s", name, strings.Join(DialectNames(), " | "))
	}
	return newDialect(), nil
}

// DialectNames 返回全部已注册的后端名（顺序固定，便于报错与清单）。
func DialectNames() []string {
	return []string{DriverPostgres, DriverSQLite}
}

// ResolveDialect 解析"后端 + 连接串"这一对。
//
// driver 为空或 auto 时按连接串形态推断：postgres:// / postgresql:// 前缀
// 即 postgres，其余（含空串）按 sqlite 算。空连接串在 sqlite 下由调用方
// 补默认路径（见 Options.DataDir）。
func ResolveDialect(driver, dsn string) (Dialect, error) {
	name := strings.ToLower(strings.TrimSpace(driver))
	if name == "" || name == DriverAuto {
		if isPostgresDSN(dsn) {
			name = DriverPostgres
		} else {
			name = DriverSQLite
		}
	}
	d, err := DialectByName(name)
	if err != nil {
		return nil, err
	}
	// 显式指定 postgres 却给了个不像 DSN 的值：不能让它掉到 sqlite 上
	// 被当成文件路径——那会造出一个文件名匪夷所思的空库。
	if d.Name() == DriverPostgres && dsn != "" && !isPostgresDSN(dsn) {
		return nil, fmt.Errorf("后端是 %s，但连接串 %q 不是 postgres:// 或 postgresql:// 开头", DriverPostgres, dsn)
	}
	return d, nil
}

func isPostgresDSN(dsn string) bool {
	lower := strings.ToLower(strings.TrimSpace(dsn))
	return strings.HasPrefix(lower, "postgres://") || strings.HasPrefix(lower, "postgresql://")
}

// migrationsFS 返回某个方言的迁移文件树。
// 迁移文件按方言分目录（migrations/postgres、migrations/sqlite），
// 两个目录各自独立演进——PG 的增量迁移历史没有理由拖累 SQLite 基线。
func migrationsFS(d Dialect) (fs.FS, error) {
	sub, err := fs.Sub(migrations, "migrations/"+d.Name())
	if err != nil {
		return nil, fmt.Errorf("找不到 %s 的迁移目录: %w", d.Name(), err)
	}
	return sub, nil
}
