package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"gorm.io/gorm"
)

// DDL 与模型现在分处两个文件（schema.sql 管建表，models.go 管读写），
// 两边一旦漂移，症状是某次查询报 "no such column" 而没有任何提示。
// 这个测试把两份列清单钉死在一起。
func TestSchemaMatchesModels(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	for _, tc := range []struct {
		table string
		model any
	}{
		{"servers", &serverRow{}},
		{"tools", &toolRow{}},
		{"call_logs", &logRow{}},
		{"credentials", &credentialRow{}},
		{"api_keys", &apiKeyRow{}},
	} {
		inDDL := ddlColumns(t, schemaSQL, tc.table)
		inModel := modelColumns(t, s.GORM(), tc.model)

		if got, want := strings.Join(inModel, ","), strings.Join(inDDL, ","); got != want {
			t.Errorf("%s 的列不一致（schema.sql vs 模型）:\n  DDL  : %v\n  模型 : %v",
				tc.table, inDDL, inModel)
		}

		// 顺带确认建出来的库里确实是这些列——DDL 里少一个列，
		// 光比对文本是看不出来的。这一路只比集合，因为建表顺序
		// 不是契约（PG 与 SQLite 的返回顺序还不一样）。
		if got, want := liveColumns(t, ctx, s, tc.table), sorted(inDDL); !equalSlices(got, want) {
			t.Errorf("%s 实际建表结果与 schema.sql 不一致:\n  DDL  : %v\n  实建 : %v",
				tc.table, want, got)
		}
	}
}

// 老库（v1）升上来时，新增的两列必须被补上，且原有数据不能丢。
func TestMigrateAddsV2Columns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v1.db")

	raw, err := sql.Open("sqlite", sqliteDSN(path))
	if err != nil {
		t.Fatalf("打开旧库失败: %v", err)
	}
	if _, err := raw.Exec(v1Schema); err != nil {
		t.Fatalf("建 v1 库失败: %v", err)
	}
	if _, err := raw.Exec(`INSERT INTO servers
		(id, key, name, transport, url, headers, command, args, env, timeout_ms,
		 protocol_version, legacy_only, tool_filter_mode, tool_filter_list,
		 enabled, status, protocol_era, server_name, server_version, capabilities,
		 last_check_at, last_latency_ms, last_error_kind, last_error, created_at, updated_at)
		VALUES ('s1','old','旧配置','streamable_http','http://x','{}','','[]','[]',30000,
		        '',0,'allow_all','[]',1,'healthy','modern','n','1','[]',
		        NULL,NULL,'','',1,1)`); err != nil {
		t.Fatalf("写入 v1 数据失败: %v", err)
	}
	raw.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatalf("v1 库升级失败: %v", err)
	}
	defer s.Close()
	ctx := context.Background()

	for _, col := range []struct{ table, name string }{
		{"servers", "est_tokens_per_call"},
		{"call_logs", "credential_id"},
	} {
		if got := liveColumns(t, ctx, s, col.table); !contains(got, col.name) {
			t.Errorf("%s 缺少 v2 新增列 %s，实际 = %v", col.table, col.name, got)
		}
	}

	got, err := s.GetServer(ctx, "s1")
	if err != nil {
		t.Fatalf("v1 数据读不出来: %v", err)
	}
	if got.Spec.Key != "old" || got.Status != "healthy" || !got.Enabled {
		t.Errorf("v1 数据被改坏了: %+v", got)
	}

	if v, err := s.readSchemaVersion(ctx); err != nil || v != schemaVersion {
		t.Errorf("schema 版本 = %d, err = %v, want %d", v, err, schemaVersion)
	}

	// 再开一次不能报错也不能重复 ALTER。
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("二次打开失败: %v", err)
	}
	s2.Close()
}

// TestSplitStatements 保证切分不会把注释里的内容当成语句，
// 也不会漏掉最后一条没有换行的语句。
func TestSplitStatements(t *testing.T) {
	got := splitStatements("-- 抬头注释\nCREATE TABLE a (x INTEGER);\n-- 中间注释\nCREATE TABLE b (y INTEGER)")

	want := []string{"CREATE TABLE a (x INTEGER)", "CREATE TABLE b (y INTEGER)"}
	if len(got) != len(want) {
		t.Fatalf("切出 %d 条，want %d：%#v", len(got), len(want), got)
	}
	for i := range want {
		if strings.TrimSpace(got[i]) != want[i] {
			t.Errorf("第 %d 条 = %q, want %q", i, got[i], want[i])
		}
	}
}

// --- 辅助 ---

// ddlColumns 从 schema.sql 里取出某张表的列名。
func ddlColumns(t *testing.T, script, table string) []string {
	t.Helper()

	prefix := "CREATE TABLE IF NOT EXISTS " + table + " ("
	for _, stmt := range splitStatements(script) {
		flat := strings.Join(strings.Fields(stmt), " ")
		idx := strings.Index(flat, prefix)
		if idx < 0 {
			continue
		}
		body := flat[idx+len(prefix):]
		body = strings.TrimSuffix(strings.TrimSpace(body), ")")

		var out []string
		for _, part := range strings.Split(body, ",") {
			fields := strings.Fields(strings.TrimSpace(part))
			if len(fields) < 2 {
				continue
			}
			// 跳过表级约束：UNIQUE(server_id, name) 之类。
			switch strings.ToUpper(fields[0]) {
			case "UNIQUE", "PRIMARY", "FOREIGN", "CONSTRAINT", "CHECK":
				continue
			}
			out = append(out, fields[0])
		}
		return out
	}
	t.Fatalf("schema.sql 里找不到表 %s", table)
	return nil
}

// modelColumns 取 GORM 模型映射出的列名，按模型里的声明顺序。
func modelColumns(t *testing.T, db *gorm.DB, model any) []string {
	t.Helper()

	stmt := &gorm.Statement{DB: db}
	if err := stmt.Parse(model); err != nil {
		t.Fatalf("解析模型失败: %v", err)
	}
	out := make([]string, 0, len(stmt.Schema.Fields))
	for _, f := range stmt.Schema.Fields {
		if f.DBName != "" {
			out = append(out, f.DBName)
		}
	}
	return out
}

// liveColumns 读库里某张表真实的列，返回排序后的结果。
func liveColumns(t *testing.T, ctx context.Context, s *Store, table string) []string {
	t.Helper()

	sqlDB, err := s.db.DB()
	if err != nil {
		t.Fatalf("获取连接池失败: %v", err)
	}
	rows, err := sqlDB.QueryContext(ctx, fmt.Sprintf("SELECT * FROM %s LIMIT 0", table))
	if err != nil {
		t.Fatalf("读取 %s 表结构失败: %v", table, err)
	}
	cols, err := rows.Columns()
	rows.Close()
	if err != nil {
		t.Fatalf("读取 %s 列名失败: %v", table, err)
	}

	// SQLite 返回建表顺序，PG 的返回顺序不保证；两边都按名字排一遍。
	sort.Strings(cols)
	return cols
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

func sorted(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

func equalSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// v1Schema 是 v2 之前的那一版结构：servers 无 est_tokens_per_call，
// call_logs 无 credential_id，且没有 credentials / schema_meta 两张表。
const v1Schema = `
CREATE TABLE IF NOT EXISTS servers (
  id                TEXT PRIMARY KEY,
  key               TEXT NOT NULL UNIQUE,
  name              TEXT NOT NULL,
  transport         TEXT NOT NULL,
  url               TEXT NOT NULL DEFAULT '',
  headers           TEXT NOT NULL DEFAULT '{}',
  command           TEXT NOT NULL DEFAULT '',
  args              TEXT NOT NULL DEFAULT '[]',
  env               TEXT NOT NULL DEFAULT '[]',
  timeout_ms        INTEGER NOT NULL DEFAULT 30000,
  protocol_version  TEXT NOT NULL DEFAULT '',
  legacy_only       INTEGER NOT NULL DEFAULT 0,
  tool_filter_mode  TEXT NOT NULL DEFAULT 'allow_all',
  tool_filter_list  TEXT NOT NULL DEFAULT '[]',
  enabled           INTEGER NOT NULL DEFAULT 1,
  status            TEXT NOT NULL DEFAULT 'unknown',
  protocol_era      TEXT NOT NULL DEFAULT '',
  server_name       TEXT NOT NULL DEFAULT '',
  server_version    TEXT NOT NULL DEFAULT '',
  capabilities      TEXT NOT NULL DEFAULT '[]',
  last_check_at     INTEGER,
  last_latency_ms   INTEGER,
  last_error_kind   TEXT NOT NULL DEFAULT '',
  last_error        TEXT NOT NULL DEFAULT '',
  created_at        INTEGER NOT NULL,
  updated_at        INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS call_logs (
  id            TEXT PRIMARY KEY,
  ts            INTEGER NOT NULL,
  server_id     TEXT NOT NULL DEFAULT '',
  server_key    TEXT NOT NULL DEFAULT '',
  tool_name     TEXT NOT NULL DEFAULT '',
  exposed_name  TEXT NOT NULL DEFAULT '',
  duration_ms   INTEGER NOT NULL DEFAULT 0,
  status        TEXT NOT NULL DEFAULT '',
  error_kind    TEXT NOT NULL DEFAULT '',
  error         TEXT NOT NULL DEFAULT ''
);
`
