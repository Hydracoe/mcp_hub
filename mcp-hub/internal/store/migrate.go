package store

import "embed"

// migrations 是全部方言的迁移文件树，按后端名分目录。
// postgres 为增量迁移（每次 schema 变更追加一对文件），sqlite 为合并后的基线
// （ALTER COLUMN … SET DEFAULT 等 PG 写法在 SQLite 上不被支持）。
// 两者都走 golang-migrate，版本状态记在各自的 schema_migrations 表里。
//
//go:embed all:migrations
var migrations embed.FS
