-- MCP Hub 初始 schema（由 golang-migrate 执行，版本记录在其自带的 schema_migrations 表）
--
-- 目标数据库只有 PostgreSQL（含 pgvector 扩展）。
-- 只用最朴素的存储类型（text / integer / bigint / 内联外键 / partial 索引），
-- 时间戳一律存 epoch 毫秒的 bigint（integer 32 位撑不过一个月），
-- 不碰 timestamp 与时区语义。
--
-- 刻意不用 GORM 的 AutoMigrate：它靠文本解析已有表的 DDL 判断差异，
-- 遇到行内注释会读错列清单，进而触发 DROP TABLE 式"修复"——一条
-- 会把数据写丢的路径。DDL 由迁移文件独占，GORM 只负责读写。
-- 后续 schema 变更：新增 000002_xxx.up.sql / .down.sql，不改动本文件。

CREATE TABLE servers (
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

  -- TPM 限流的预估兜底：网关发请求前拿不到 token 数，用这个值预估。0 = 不计量。
  est_tokens_per_call INTEGER NOT NULL DEFAULT 0,

  enabled           INTEGER NOT NULL DEFAULT 1,

  -- 元数据：description/tags/category_id 人工维护，instructions 由探活自动落库。
  description       TEXT NOT NULL DEFAULT '',
  tags              TEXT NOT NULL DEFAULT '[]',  -- JSON array
  instructions      TEXT NOT NULL DEFAULT '',    -- 下游 serverInfo.instructions
  category_id       TEXT NOT NULL DEFAULT '',    -- 单值分类，无外键（删分类时显式清空）

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
-- 路由仍走显式映射（server_id + name），不做字符串切分。
CREATE TABLE tools (
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
CREATE INDEX idx_tools_exposed ON tools(exposed_name);
CREATE INDEX idx_tools_server ON tools(server_id);

-- 工具调用日志。args/result 均截断落库（复现线索，不是存档）。
CREATE TABLE call_logs (
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
  -- 归因到具体密钥，单密钥模式为空串。只记 id 不记明文。
  credential_id TEXT NOT NULL DEFAULT '',
  -- 调用方（接入 API key）。api_key_name 是调用当时的名字快照——
  -- 日志是历史事实，改名或删除不该改写过去的归因。
  api_key_id    TEXT NOT NULL DEFAULT '',
  api_key_name  TEXT NOT NULL DEFAULT '',
  args          TEXT NOT NULL DEFAULT '',   -- JSON，工具入参
  result        TEXT NOT NULL DEFAULT ''    -- 工具返回（JSON 序列化）
);
CREATE INDEX idx_logs_ts ON call_logs(ts DESC);

-- 接入 API key：外部 AI 客户端调用 /mcp 端点的凭据。
-- 只存 sha256，明文只在创建响应里出现一次。
CREATE TABLE api_keys (
  id           TEXT PRIMARY KEY,
  name         TEXT NOT NULL,                 -- 标识调用方，如 "claude-desktop"
  prefix       TEXT NOT NULL DEFAULT '',      -- 明文前 8 位，列表展示用
  key_hash     TEXT NOT NULL UNIQUE,          -- sha256 hex
  enabled      INTEGER NOT NULL DEFAULT 1,
  last_used_at BIGINT NOT NULL DEFAULT 0,
  created_at   BIGINT NOT NULL,
  updated_at   BIGINT NOT NULL
);

-- 下游密钥池。一行 = 一组完整的注入头，而不是"某个头字段"——
-- 只有整组才是一次可独立轮换的最小单位（Bearer / X-API-Key / 多头组合）。
CREATE TABLE credentials (
  id            TEXT PRIMARY KEY,
  server_id     TEXT NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
  label         TEXT NOT NULL DEFAULT '',
  headers       TEXT NOT NULL DEFAULT '{}',   -- JSON object，完整注入头

  enabled       INTEGER NOT NULL DEFAULT 1,
  -- 持久状态只有 active / disabled。cooling（限流冷却）是内存态，刻意不落库：
  -- 重启往往就是为了恢复，落库只会让新进程继续背旧账。
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
CREATE INDEX idx_creds_server ON credentials(server_id, sort_order);

-- label 在同一 server 内唯一（两行都叫"主号"的密钥，用户无法在日志与
-- 界面之间对上号）；空 label 不参与约束。
CREATE UNIQUE INDEX idx_creds_label
  ON credentials(server_id, label) WHERE label <> '';

-- 分类。单值：一个 server 至多归属一个分类，多标签需求由 servers.tags 承担。
-- name 是中文展示名，slug 是英文名，刻意分开：slug 进对外工具名的第一段
-- （<slug>__<serverKey>__<toolName>），必须是 ASCII；中文名折叠后只剩空串。
-- slug 唯一走 partial 索引：空 slug 走 id 回退，不算冲突。
CREATE TABLE categories (
  id            TEXT PRIMARY KEY,
  name          TEXT NOT NULL UNIQUE,      -- 中文名：展示 + 自动归类匹配
  slug          TEXT NOT NULL DEFAULT '',  -- 英文名：进工具名的前缀段
  description   TEXT NOT NULL DEFAULT '',  -- 展示 + 自动归类匹配
  created_at    BIGINT NOT NULL,
  updated_at    BIGINT NOT NULL
);
CREATE UNIQUE INDEX idx_categories_slug ON categories(slug) WHERE slug <> '';

-- 工具向量（hybrid 检索的向量一路）。
-- vector 刻意存 TEXT（JSON 数组）而不是 pgvector 的 vector 类型：
-- 表的读写不依赖扩展装没装上；查询侧用 `vector::vector <=> $1::vector` 强转做 ANN。
-- 主键是完整代身份 (tool_id, model, dims, spec)：换模型/换语料字段即新代，
-- 新代整代建好再原子切换（热切换），期间新旧两代并存。
CREATE TABLE tool_embeddings (
  tool_id       TEXT NOT NULL REFERENCES tools(id) ON DELETE CASCADE,
  model         TEXT NOT NULL,             -- embed 模型名
  dims          INTEGER NOT NULL,
  spec          TEXT NOT NULL,             -- 语料字段组合编码（见 search/corpus.SpecString）
  vector        TEXT NOT NULL,             -- JSON 数组
  corpus_hash   TEXT NOT NULL,             -- hash(语料文本 + model + dims)，增量重建判定
  created_at    BIGINT NOT NULL,
  PRIMARY KEY (tool_id, model, dims, spec)
);
