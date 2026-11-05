-- 工具级管控：新探活发现的工具默认禁用，必须经人工启用才进入聚合端点。
-- ALTER COLUMN SET DEFAULT 不影响存量行：已有行保持 enabled=1，升级不该把线上能力悄悄关掉。
ALTER TABLE tools ALTER COLUMN enabled SET DEFAULT 0;

-- 2) 工具变更记录：探活对账（added/removed/updated）与人工启停
--    （enabled/disabled）都留痕，回答"这个工具什么时候、因为什么没了"。
CREATE TABLE tool_changes (
  id          TEXT PRIMARY KEY,
  server_id   TEXT NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
  -- server_key 是删除前的名字快照：行随 server 级联删除，但存活期间
  -- 界面不必为了展示来源再 join 一次 servers。
  server_key  TEXT NOT NULL DEFAULT '',
  tool_name   TEXT NOT NULL,
  -- added：探活新发现；removed：上游已删除；updated：描述/schema 变化；
  -- enabled/disabled：人工启停。
  action      TEXT NOT NULL,
  detail      TEXT NOT NULL DEFAULT '',
  -- system=探活对账自动写入；admin=管理接口人工操作。
  actor       TEXT NOT NULL DEFAULT '',
  ts          BIGINT NOT NULL
);
CREATE INDEX idx_tool_changes_ts ON tool_changes(ts DESC);
CREATE INDEX idx_tool_changes_server_ts ON tool_changes(server_id, ts DESC);
