-- MCP Hub schema
--
-- server 配置、聚合工具缓存、调用日志、密钥池。
--
-- 本文件是 PostgreSQL 与 SQLite 共用的：只用两边都认的语法
-- （text / integer / bigint / IF NOT EXISTS / 内联外键 / 部分索引 / DESC 索引），
-- 时间戳一律存 epoch 毫秒的整数，不碰 timestamp 与时区语义。
-- 毫秒时间戳必须用 bigint：SQLite 的 integer 是 64 位没问题，但
-- PostgreSQL 的 integer 只有 32 位，1970 后第 24.8 天就溢出了。
--
-- 版本变更走 migrate()：新库直接按本文件建全，老库按 schemaVersion
-- 补增量。CREATE TABLE IF NOT EXISTS 对已存在的表是空操作，因此
-- 给旧表加列必须在 migrate() 里显式 ALTER。
--
-- 刻意不用 GORM 的 AutoMigrate：它在 SQLite 上要靠文本解析已有表的
-- DDL 来判断差异，遇到本文件里这些行内注释就会把列清单读错，
-- 进而触发一次 DROP TABLE + RENAME 的"修复"——那是一条会把数据
-- 写丢的路径。DDL 由本文件独占，GORM 只负责读写。

-- 版本记录。用一张表而不是 PRAGMA user_version：后者只有 SQLite 有。
CREATE TABLE IF NOT EXISTS schema_meta (
  id      INTEGER PRIMARY KEY,
  version INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS servers (
  id                TEXT PRIMARY KEY,
  key               TEXT NOT NULL UNIQUE,        -- 工具前缀，如 github
  name              TEXT NOT NULL,
  transport         TEXT NOT NULL,               -- streamable_http | sse | stdio

  url               TEXT NOT NULL DEFAULT '',
  headers           TEXT NOT NULL DEFAULT '{}',  -- JSON object
  auth_mode         TEXT NOT NULL DEFAULT '',    -- 空=按 headers 推断 | none | header
  command           TEXT NOT NULL DEFAULT '',
  args              TEXT NOT NULL DEFAULT '[]',  -- JSON array
  env               TEXT NOT NULL DEFAULT '[]',  -- JSON array

  timeout_ms        INTEGER NOT NULL DEFAULT 30000,
  protocol_version  TEXT NOT NULL DEFAULT '',    -- 空 = 自动协商
  legacy_only       INTEGER NOT NULL DEFAULT 0,

  tool_filter_mode  TEXT NOT NULL DEFAULT 'allow_all',  -- allow_all | allow | block
  tool_filter_list  TEXT NOT NULL DEFAULT '[]',         -- JSON array

  -- TPM 的中间回退：网关发请求前拿不到 token 数，用这个值预估。
  -- 0 = 不计量（比"记成 0"诚实，见 PRD §7.5.7）。
  est_tokens_per_call INTEGER NOT NULL DEFAULT 0,

  enabled           INTEGER NOT NULL DEFAULT 1,

  -- 元数据（PRD §4.7）：description/tags 人工可选；instructions 探活自动落库。
  description       TEXT NOT NULL DEFAULT '',
  tags              TEXT NOT NULL DEFAULT '[]',  -- JSON array
  instructions      TEXT NOT NULL DEFAULT '',    -- 下游 serverInfo.instructions
  category_id       TEXT NOT NULL DEFAULT '',    -- 分类，单值，无外键（删分类时清空引用）

  -- 最近一次探活结果
  status            TEXT NOT NULL DEFAULT 'unknown',    -- healthy | degraded | offline | unknown
  protocol_era      TEXT NOT NULL DEFAULT '',           -- modern | legacy
  server_name       TEXT NOT NULL DEFAULT '',
  server_version    TEXT NOT NULL DEFAULT '',
  capabilities      TEXT NOT NULL DEFAULT '[]',         -- JSON array
  last_check_at     BIGINT,
  last_latency_ms   INTEGER,
  last_error_kind   TEXT NOT NULL DEFAULT '',
  last_error        TEXT NOT NULL DEFAULT '',

  created_at        BIGINT NOT NULL,
  updated_at        BIGINT NOT NULL
);

-- 工具缓存：exposed_name 是 <serverKey>__<toolName> 的物化结果，
-- 但路由仍走显式映射（server_id + name），不做字符串切分。
CREATE TABLE IF NOT EXISTS tools (
  id            TEXT PRIMARY KEY,
  server_id     TEXT NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
  name          TEXT NOT NULL,              -- 下游原始名
  exposed_name  TEXT NOT NULL,              -- serverKey__name
  description   TEXT NOT NULL DEFAULT '',
  input_schema  TEXT NOT NULL DEFAULT '{}', -- JSON Schema
  enabled       INTEGER NOT NULL DEFAULT 1,
  cached_at     BIGINT NOT NULL,
  UNIQUE(server_id, name)
);
CREATE INDEX IF NOT EXISTS idx_tools_exposed ON tools(exposed_name);
CREATE INDEX IF NOT EXISTS idx_tools_server ON tools(server_id);

CREATE TABLE IF NOT EXISTS call_logs (
  id            TEXT PRIMARY KEY,
  ts            BIGINT NOT NULL,
  server_id     TEXT NOT NULL DEFAULT '',
  server_key    TEXT NOT NULL DEFAULT '',
  tool_name     TEXT NOT NULL DEFAULT '',
  exposed_name  TEXT NOT NULL DEFAULT '',
  duration_ms   INTEGER NOT NULL DEFAULT 0,
  status        TEXT NOT NULL DEFAULT '',   -- ok | error
  error_kind    TEXT NOT NULL DEFAULT '',
  error         TEXT NOT NULL DEFAULT '',
  -- 归因到具体密钥。单密钥模式（无 credentials）为空串。
  -- 只记 id 不记明文：日志里落一份凭据等于把密钥抄进了另一个地方。
  credential_id TEXT NOT NULL DEFAULT '',
  -- 调用方（接入 API key）与请求现场。均截断落库，超长部分丢弃。
  api_key_id    TEXT NOT NULL DEFAULT '',
  args          TEXT NOT NULL DEFAULT '',   -- JSON，工具入参
  result        TEXT NOT NULL DEFAULT ''    -- 工具返回（JSON 序列化）
);
CREATE INDEX IF NOT EXISTS idx_logs_ts ON call_logs(ts DESC);

-- 接入 API key：外部 AI 客户端调用 /mcp 端点的凭据。
-- 只存 sha256，明文只在创建响应里出现一次。
CREATE TABLE IF NOT EXISTS api_keys (
  id           TEXT PRIMARY KEY,
  name         TEXT NOT NULL,                 -- 标识调用方，如 "claude-desktop"
  prefix       TEXT NOT NULL DEFAULT '',      -- 明文前 8 位，列表展示用
  key_hash     TEXT NOT NULL UNIQUE,          -- sha256 hex
  enabled      INTEGER NOT NULL DEFAULT 1,
  last_used_at BIGINT NOT NULL DEFAULT 0,
  created_at   BIGINT NOT NULL,
  updated_at   BIGINT NOT NULL
);

-- 密钥池。一行 = 一组完整的注入头，而不是"某个头字段"——
-- 供应商的凭据形态各不相同（Bearer / X-API-Key / 多个头组合），
-- 只有整组才是一次可独立轮换的最小单位。
CREATE TABLE IF NOT EXISTS credentials (
  id            TEXT PRIMARY KEY,
  server_id     TEXT NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
  label         TEXT NOT NULL DEFAULT '',
  headers       TEXT NOT NULL DEFAULT '{}',   -- JSON object，完整注入头

  enabled       INTEGER NOT NULL DEFAULT 1,
  -- 持久状态只有 active / disabled 两种。cooling 是内存态，
  -- 刻意不落库：重启往往就是为了恢复，把限流状态持久化
  -- 只会让新进程继续背旧账（PRD §7.5.3）。
  state         TEXT NOT NULL DEFAULT 'active',
  disabled_kind TEXT NOT NULL DEFAULT '',     -- INVALID_KEY | KEY_REVOKED | QUOTA_EXHAUSTED
  disabled_at   BIGINT,
  disabled_note TEXT NOT NULL DEFAULT '',

  rpm_limit     INTEGER NOT NULL DEFAULT 0,   -- 每分钟请求上限，0 = 不限
  tpm_limit     INTEGER NOT NULL DEFAULT 0,   -- 每分钟 token 上限，0 = 不限

  sort_order    INTEGER NOT NULL DEFAULT 0,
  created_at    BIGINT NOT NULL,
  updated_at    BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_creds_server ON credentials(server_id, sort_order);

-- label 在同一 server 内唯一：界面上两行都叫"主号"的密钥，
-- 用户无法在日志与界面之间对上号。空 label 不参与约束
-- ——允许用户只填密钥不填名字。
CREATE UNIQUE INDEX IF NOT EXISTS idx_creds_label
  ON credentials(server_id, label) WHERE label <> '';

-- 分类（PRD §4.7.3）。单值：一个 server 恰属一个分类；
-- 多标签需求由 servers.tags 承担，这里不建关联表。
CREATE TABLE IF NOT EXISTS categories (
  id            TEXT PRIMARY KEY,
  name          TEXT NOT NULL UNIQUE,
  description   TEXT NOT NULL DEFAULT '',  -- 展示 + 自动归类匹配
  created_at    BIGINT NOT NULL,
  updated_at    BIGINT NOT NULL
);

-- 工具向量（PRD §4.7.5）。
-- vector 列刻意存 TEXT（JSON 数组）而不是 pgvector 的 vector 类型：
-- 本文件是 PG 与 SQLite 共用的，vector 类型只有 PG 有；查询侧在 PG 上
-- 用 `vector::vector <=> $1::vector` 强转做 ANN，SQLite 方言下整条
-- 向量检索链路关闭（降级 keyword/bm25），这张表只写不读。
CREATE TABLE IF NOT EXISTS tool_embeddings (
  tool_id       TEXT PRIMARY KEY REFERENCES tools(id) ON DELETE CASCADE,
  model         TEXT NOT NULL,             -- embed 模型名
  dims          INTEGER NOT NULL,
  vector        TEXT NOT NULL,             -- JSON 数组
  corpus_hash   TEXT NOT NULL,             -- hash(语料文本 + model)，增量重建判定
  created_at    BIGINT NOT NULL
);

-- 运行时配置（PRD §4.7.5）。key-value，value 为 JSON 字符串。
CREATE TABLE IF NOT EXISTS app_config (
  key           TEXT PRIMARY KEY,          -- 如 'embed'
  value         TEXT NOT NULL
);
