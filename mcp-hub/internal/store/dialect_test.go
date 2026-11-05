package store_test

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/hydra/mcp-hub/internal/store"
)

// 两种后端都是"生产的后端"：PostgreSQL 用于服务端/多实例，SQLite 用于桌面/单机，
// 两边都必须满足同一套最基本的使用契约。本文件只测 SQLite，CI 上无需外部依赖。

// newStore 按指定后端开一个库。
func newStore(t *testing.T, driver, dsn, dataDir string) *store.Store {
	t.Helper()
	st, err := store.OpenWith(store.Options{Driver: driver, DSN: dsn, DataDir: dataDir})
	if err != nil {
		t.Fatalf("打开 %s 库失败: %v", driver, err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// sqliteStore 开一个临时文件库。
func sqliteStore(t *testing.T, name string) (*store.Store, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	// 传文件路径而不是 DataDir：这样每个用例一个库文件，互不干扰。
	return newStore(t, store.DriverSQLite, path, dir), path
}

func TestSQLiteDialectEngine(t *testing.T) {
	st, _ := sqliteStore(t, "engine.db")
	if got := st.Dialect().Name(); got != store.DriverSQLite {
		t.Fatalf("方言 = %q，期望 %q", got, store.DriverSQLite)
	}
	if st.Dialect().NativeVectorSearch() {
		t.Error("SQLite 不该声称有原生向量检索")
	}
	if got := st.Dialect().LikeOp(); got != "LIKE" {
		t.Errorf("LikeOp = %q，期望 LIKE", got)
	}
}

// DSN 上的 PRAGMA 必须真的生效。三条都不是可选优化：
// foreign_keys 关掉级联删除会静默失效；非 WAL 时一次写会挡住所有读；
// busy_timeout 为 0 时并发写立刻返回 SQLITE_BUSY 而不是等待。
func TestSQLitePragmasEffective(t *testing.T) {
	st, _ := sqliteStore(t, "pragma.db")

	sqlDB, err := st.DB().DB()
	if err != nil {
		t.Fatalf("取连接池失败: %v", err)
	}
	for _, c := range []struct {
		pragma string
		want   string
	}{
		{"foreign_keys", "1"},
		{"journal_mode", "wal"},
	} {
		var got string
		if err := sqlDB.QueryRow("PRAGMA " + c.pragma).Scan(&got); err != nil {
			t.Fatalf("PRAGMA %s 查询失败: %v", c.pragma, err)
		}
		// journal_mode 是小写回显，foreign_keys 是 "1"。
		if got != c.want {
			t.Errorf("PRAGMA %s = %q，期望 %q", c.pragma, got, c.want)
		}
	}

	var timeout int
	if err := sqlDB.QueryRow("PRAGMA busy_timeout").Scan(&timeout); err != nil {
		t.Fatalf("PRAGMA busy_timeout 查询失败: %v", err)
	}
	if timeout <= 0 {
		t.Errorf("PRAGMA busy_timeout = %d，期望 > 0（0 表示写冲突立刻失败）", timeout)
	}
}

// 外键级联必须真的生效：tools 随 servers 级联删除是"删上游不留残渣"的保证。
// 这里绕开领域 Store 直接写 SQL，才验得出数据库本身；PRAGMA 只在连接建立时生效，
// 所以同时验证了每个新连接都拿到正确的 PRAGMA。
func TestSQLiteForeignKeyCascade(t *testing.T) {
	st, _ := sqliteStore(t, "fk.db")
	ctx := t.Context()

	now := time.Now().UnixMilli()
	if err := st.DB().WithContext(ctx).Exec(
		`INSERT INTO servers (id, key, name, transport, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		"srv1", "srv1", "Srv 1", "stdio", now, now).Error; err != nil {
		t.Fatalf("插入 server 失败: %v", err)
	}
	if err := st.DB().WithContext(ctx).Exec(
		`INSERT INTO tools (id, server_id, name, exposed_name, cached_at) VALUES (?, ?, ?, ?, ?)`,
		"tool1", "srv1", "alpha", "srv1__alpha", now).Error; err != nil {
		t.Fatalf("插入 tool 失败: %v", err)
	}

	if err := st.DB().WithContext(ctx).Exec(`DELETE FROM servers WHERE id = ?`, "srv1").Error; err != nil {
		t.Fatalf("删除 server 失败: %v", err)
	}

	var n int64
	if err := st.DB().WithContext(ctx).Raw(`SELECT count(*) FROM tools`).Scan(&n).Error; err != nil {
		t.Fatalf("统计 tools 失败: %v", err)
	}
	if n != 0 {
		t.Fatalf("级联删除没生效：还剩 %d 个工具行（PRAGMA foreign_keys 没打开？）", n)
	}
}

// 并发写同一张表最容易踩到 SQLite 锁问题：形状与引擎里"并发探活 → 各自落库"一致，
// 每个写事务先 SELECT 再写。deferred 事务会以 SQLITE_BUSY_SNAPSHOT 失败且 busy_timeout 无效，
// 连接串上的 _txlock=immediate 就是为它准备的（见 dialect_sqlite.go）。
func TestSQLiteConcurrentReadThenWriteTx(t *testing.T) {
	st, _ := sqliteStore(t, "concurrent.db")
	ctx := t.Context()

	now := time.Now().UnixMilli()
	for i := 0; i < 8; i++ {
		if err := st.DB().WithContext(ctx).Exec(
			`INSERT INTO servers (id, key, name, transport, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
			fmt.Sprintf("srv%d", i), fmt.Sprintf("srv%d", i), "S", "stdio", now, now).Error; err != nil {
			t.Fatalf("准备数据失败: %v", err)
		}
	}

	const rounds = 10
	var wg sync.WaitGroup
	errCh := make(chan error, 8*rounds)

	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				id := fmt.Sprintf("srv%d", w)
				name := fmt.Sprintf("round-%d", i)
				err := st.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
					var row struct{ Name string }
					if err := tx.Raw(`SELECT name FROM servers WHERE id = ?`, id).Scan(&row).Error; err != nil {
						return err
					}
					return tx.Exec(`UPDATE servers SET name = ?, updated_at = ? WHERE id = ?`,
						name, time.Now().UnixMilli(), id).Error
				})
				if err != nil {
					errCh <- fmt.Errorf("writer %d round %d: %w", w, i, err)
					return
				}
			}
		}(w)
	}
	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Error(err)
	}
}
