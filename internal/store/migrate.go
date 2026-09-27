package store

import "embed"

// migrations 是全部方言的迁移文件树，按后端名分目录：
//
//	migrations/postgres/   PostgreSQL 的增量迁移历史（000001… 逐一叠加）
//	migrations/sqlite/     SQLite 的基线迁移（当前 schema 一次到位）
//
// 两个目录各自独立演进。PostgreSQL 那份是历史积累，每加一次 schema 变更
// 就追加一对文件；SQLite 那份是"新建的部署形态"，没有历史包袱可背，
// 所以直接是合并后的基线——重放 6 个 PG 增量迁移里的
// `ALTER COLUMN … SET DEFAULT` 在 SQLite 上根本不被支持，
// 与其为历史写兼容层，不如让新形态从一个干净的 schema 开始。
//
// 两者共同的部分是"迁移机制"：都走 golang-migrate，
// 版本状态都记在各自的 schema_migrations 表里。
//
//go:embed all:migrations
var migrations embed.FS
