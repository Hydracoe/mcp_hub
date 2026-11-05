// Package storetest 为测试提供一次性的数据库，后端由 MCP_HUB_TEST_DRIVER 选择（默认 postgres）。
// postgres 新建唯一命名的库、用完 DROP（MCP_HUB_TEST_DSN 需有 CREATE DATABASE 权限）；sqlite 在 t.TempDir() 下建文件库。
// 两者都天然支持 t.Parallel 与跨包并发；调用方用 store.Open(storetest.DSN(t))。
package storetest

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	// gorm.io/driver/postgres 内部 import 了 pgx stdlib，其 init 会向 database/sql
	// 注册 "pgx" 驱动。借道 gorm 注册即可，pgx 在 go.mod 中保持 indirect。
	_ "gorm.io/driver/postgres"
)

// 后端名。与 store 包的取值一致；测试基建不该让被测包成为编译期依赖方向。
const (
	DriverPostgres = "postgres"
	DriverSQLite   = "sqlite"
)

// EnvDriver 是选择测试后端的变量名。
const EnvDriver = "MCP_HUB_TEST_DRIVER"

// fallbackDSN is the admin DSN used when MCP_HUB_TEST_DSN is not set.
// It matches the postgres service in the top-level docker-compose.yml.
const fallbackDSN = "postgres://mcphub:mcphub@127.0.0.1:5432/postgres?sslmode=disable"

// Driver 返回本次测试使用的后端名。未设置时用 postgres——CI 上默认跑最严格的那个。
func Driver() string {
	switch d := strings.ToLower(strings.TrimSpace(os.Getenv(EnvDriver))); d {
	case DriverSQLite:
		return DriverSQLite
	case DriverPostgres, "":
		return DriverPostgres
	default:
		// 认不出来的值不猜：当 postgres 处理，让连接错误暴露在第一个用例里，
		// 而不是静默换后端把方言差异藏起来。
		return DriverPostgres
	}
}

// DSN creates a fresh database and returns a connection string for it.
//
// Cleanup drops the database (postgres) or removes the file (sqlite).
// Call store.Open on the result.
func DSN(t *testing.T) string {
	t.Helper()
	if Driver() == DriverSQLite {
		return sqliteDSN(t)
	}
	return postgresDSN(t)
}

// DataDir 返回该后端下"数据目录"的概念位置。
//
// SQLite 后端要给 store.Options.DataDir 一个值；测试里就是临时目录，
// 与 DSN 给的文件同目录，语义一致。
func DataDir(t *testing.T) string {
	t.Helper()
	if Driver() == DriverSQLite {
		return t.TempDir()
	}
	return ""
}

// sqliteDSN 在临时目录里建一个空的库文件路径。
//
// 刻意是先建目录、不建文件：store 会自己创建文件并跑迁移，
// 这里多建一个空文件反而会掩盖"路径写错"这类问题。
func sqliteDSN(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "mcp-hub-test.db")
}

// postgresDSN creates a uniquely named database on the test server.
func postgresDSN(t *testing.T) string {
	t.Helper()

	name := "mcp_hub_test_" + randHex(t, 8)
	admin := openAdmin(t)

	t.Cleanup(func() {
		// WITH (FORCE) terminates lingering connections; a test that
		// leaks a store must not wedge the cleanup behind it.
		_, err := admin.Exec(fmt.Sprintf(`DROP DATABASE IF EXISTS %s WITH (FORCE)`, quoteIdent(name)))
		if err != nil {
			t.Logf("storetest: drop test database %s: %v", name, err)
		}
		admin.Close()
	})

	_, err := admin.Exec(fmt.Sprintf(`CREATE DATABASE %s`, quoteIdent(name)))
	if err != nil {
		t.Fatalf("storetest: create test database %s: %v", name, err)
	}

	u, err := url.Parse(baseDSN())
	if err != nil {
		t.Fatalf("storetest: parse base DSN: %v", err)
	}
	u.Path = "/" + name
	return u.String()
}

// openAdmin opens a connection to the administrative database named in
// the base DSN (defaulting to "postgres" when the path is empty).
func openAdmin(t *testing.T) *sql.DB {
	t.Helper()

	u, err := url.Parse(baseDSN())
	if err != nil {
		t.Fatalf("storetest: parse base DSN: %v", err)
	}
	if strings.TrimPrefix(u.Path, "/") == "" {
		u.Path = "/postgres"
	}

	db, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Fatalf("storetest: open admin connection: %v", err)
	}
	db.SetMaxOpenConns(1)

	ctx, cancel := contextWithTimeout()
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		t.Fatalf("storetest: ping test postgres (%s) failed: %v — set MCP_HUB_TEST_DSN to a reachable server, or run with %s=sqlite",
			u.Redacted(), err, EnvDriver)
	}
	return db
}

func baseDSN() string {
	if v := os.Getenv("MCP_HUB_TEST_DSN"); v != "" {
		return v
	}
	return fallbackDSN
}

func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

func randHex(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("storetest: rand: %v", err)
	}
	return hex.EncodeToString(b)
}

func contextWithTimeout() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 5*time.Second)
}
