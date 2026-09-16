// Package storetest provides throwaway PostgreSQL databases for tests.
//
// Each call creates a uniquely named database on the test server, so
// tests run hermetically: no shared schema state, safe under t.Parallel
// and across packages. Databases are dropped on cleanup.
//
// The server is taken from MCP_HUB_TEST_DSN; when unset it falls back to
// the docker-compose default (postgres://mcphub:mcphub@127.0.0.1:5432).
// The DSN must point at an administrative database the user is allowed
// to CREATE DATABASE on.
package storetest

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	// gorm.io/driver/postgres 内部 import 了 pgx stdlib，其 init 会向
	// database/sql 注册 "pgx" 驱动。借道 gorm 注册即可，无需让 pgx
	// 成为本模块的直接依赖（go.mod 中保持 indirect）。
	_ "gorm.io/driver/postgres"
)

// fallbackDSN is the admin DSN used when MCP_HUB_TEST_DSN is not set.
// It matches the postgres service in the top-level docker-compose.yml.
const fallbackDSN = "postgres://mcphub:mcphub@127.0.0.1:5432/postgres?sslmode=disable"

// DSN creates a fresh database and returns a connection string for it.
//
// Cleanup drops the database. Call store.Open on the result.
func DSN(t *testing.T) string {
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

	ctx, cancel := contextWithTimeout(t)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		t.Fatalf("storetest: ping test postgres (%s) failed: %v — set MCP_HUB_TEST_DSN to a reachable server", u.Redacted(), err)
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

func contextWithTimeout(t *testing.T) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 5*time.Second)
}
