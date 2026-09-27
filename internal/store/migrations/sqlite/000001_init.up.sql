-- MCP Hub 初始 schema（SQLite 基线）
--
-- 这份文件是 PostgreSQL 那 6 个增量迁移合并后的**当前** schema，
-- 不是历史重放。理由见 migrations/postgres 的说明与 store/migrate.go：
-- SQLite 是后来新增的部署形态，没有历史数据要兼容，所以从一个干净的
-- 基线开始；此后的 schema 变更再按 000002_xxx 往后追加。
--
-- 与 PostgreSQL 版的差异都是语言本身的限制，不是设计取舍：
--   * ALTER COLUMN / DROP COLUMN IF EXISTS 在 SQLite 上要么不支持、
--     要么要整表重建，所以直接写成目标形态；
--   * 布尔一律用 INTEGER 0/1（与 PG 版一致，GORM 侧也一样）；
--   * 时间戳一律 epoch 毫秒的 INTEGER（与 PG 版一致，不碰 timestamp 语义）。
--
-- 外键、级联删除、partial 索引 SQLite 都支持，但外键需要
-- PRAGMA foreign_keys=ON —— 这一条由 dialect_sqlite.go 写在连接串上，
-- 每个新连接自动生效，不依赖任何调用方记得打开。
--
-- 刻意不用 GORM 的 AutoMigrate：它靠文本解析已有表的 DDL 判断差异，
-- 遇到行内注释会读错列清单，进而触发 DROP TABLE 式"修复"——一条
-- 会把数据写丢的路径。DDL 由迁移文件独占，GORM 只负责读写。

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

  -- 创建时的一次性引导名单：首次探活落库时名单内的工具直接 enabled=1，
  -- 随后清空；此后启停只走 /tools/:name/enable|disable。
  boot_enabled_tools TEXT NOT NULL DEFAULT '[]', -- JSON array

  -- 新工具自动启用（默认关：上游随手加一个工具就裸奔到 AI 面前，
  -- 风险由用户承担；对信任的上游可以按 server 打开）。
  auto_enable       INTEGER NOT NULL DEFAULT 0,

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
  last_check_at     INTEGER,
  last_latency_ms   INTEGER,
  last_error_kind   TEXT NOT NULL DEFAULT '',
  last_error        TEXT NOT NULL DEFAULT '',

  created_at        INTEGER NOT NULL,
  updated_at        INTEGER NOT NULL
);

-- 工具缓存：exposed_name 是 <serverKey>__<toolName> 的物化结果，
-- 路由仍走显式映射（server_id + name），不做字符串切分。
-- enabled 默认 0：探活新发现的工具必须经人工启用才进入聚合端点。
CREATE TABLE tools (
  id            TEXT PRIMARY KEY,
  server_id     TEXT NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
  name          TEXT NOT NULL,              -- 下游原始名
  exposed_name  TEXT NOT NULL,              -- serverKey__name
  description   TEXT NOT NULL DEFAULT '',
  input_schema  TEXT NOT NULL DEFAULT '{}', -- JSON Schema
  enabled       INTEGER NOT NULL DEFAULT 0,
  cached_at     INTEGER NOT NULL,
  UNIQUE(server_id, name)
);
CREATE INDEX idx_tools_exposed ON tools(exposed_name);
CREATE INDEX idx_tools_server ON tools(server_id);

-- 工具变更记录：探活对账（added/removed/updated）与人工启停
-- （enabled/disabled）都留痕，回答"这个工具什么时候、因为什么没了"。
CREATE TABLE tool_changes (
  id          TEXT PRIMARY KEY,
  server_id   TEXT NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
  -- server_key 是删除前的名字快照：行随 server 级联删除，但存活期间
  -- 界面不必为了展示来源再 join 一次 servers。
  server_key  TEXT NOT NULL DEFAULT '',
  tool_name   TEXT NOT NULL,
  action      TEXT NOT NULL,                -- added | removed | updated | enabled | disabled
  detail      TEXT NOT NULL DEFAULT '',
  actor       TEXT NOT NULL DEFAULT '',     -- system=探活对账；admin=人工操作
  ts          INTEGER NOT NULL
);
CREATE INDEX idx_tool_changes_ts ON tool_changes(ts DESC);
CREATE INDEX idx_tool_changes_server_ts ON tool_changes(server_id, ts DESC);

-- 工具调用日志。args/result 均截断落库（复现线索，不是存档）。
CREATE TABLE call_logs (
  id            TEXT PRIMARY KEY,
  ts            INTEGER NOT NULL,
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
  last_used_at INTEGER NOT NULL DEFAULT 0,
  created_at   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL
);

-- 下游密钥池。一行 = 一组完整的注入头，而不是"某个头字段"——
-- 只有整组才是一次可独立轮换的最小单位（Bearer / X-API-Key / 多头组合）。
CREATE TABLE credentials (
  id            TEXT PRIMARY KEY,
  server_id     TEXT NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
  label         TEXT NOT NULL DEFAULT '',
  headers       TEXT NOT NULL DEFAULT '{}',   -- JSON object，完整注入头

  enabled       INTEGER NOT NULL DEFAULT 1,
  -- 持久状态只有 active / disabled。
  state         TEXT NOT NULL DEFAULT 'active',
  disabled_kind TEXT NOT NULL DEFAULT '',     -- INVALID_KEY | KEY_REVOKED | QUOTA_EXHAUSTED
  disabled_at   INTEGER,
  disabled_note TEXT NOT NULL DEFAULT '',

  -- 限流冷却只落"冷却到点时刻"，不落退避进度：重启会重置进度是可接受的，
  -- 而"这把密钥此刻还在被限"是事实，值得记住。
  -- NULL 或过去的时间戳都表示"没在冷却"。
  cooling_until INTEGER,

  rpm_limit     INTEGER NOT NULL DEFAULT 0,   -- 每分钟请求上限，0 = 不限
  sort_order    INTEGER NOT NULL DEFAULT 0,
  created_at    INTEGER NOT NULL,
  updated_at    INTEGER NOT NULL
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
  created_at    INTEGER NOT NULL,
  updated_at    INTEGER NOT NULL
);
CREATE UNIQUE INDEX idx_categories_slug ON categories(slug) WHERE slug <> '';

-- 工具向量（hybrid 检索的向量一路）。
-- vector 存 TEXT（JSON 数组）：表的读写不依赖任何扩展，SQLite 下
-- 检索整条走 Go 侧内存计算（没有 pgvector、也不引入 sqlite-vec）。
-- 主键是完整代身份 (tool_id, model, dims, spec)：换模型/换语料字段即
-- 新代，新代整代建好再原子切换（热切换），期间新旧两代并存。
CREATE TABLE tool_embeddings (
  tool_id       TEXT NOT NULL REFERENCES tools(id) ON DELETE CASCADE,
  model         TEXT NOT NULL,             -- embed 模型名
  dims          INTEGER NOT NULL,
  spec          TEXT NOT NULL,             -- 语料字段组合编码（见 search/corpus.SpecString）
  vector        TEXT NOT NULL,             -- JSON 数组
  corpus_hash   TEXT NOT NULL,             -- hash(语料文本 + model + dims)，增量重建判定
  created_at    INTEGER NOT NULL,
  PRIMARY KEY (tool_id, model, dims, spec)
);
