-- 新工具自动启用开关（O5）。
--
-- 默认的安全语义是"上游新增的工具先人工看过再放行"（T1）：上游随手
-- 加一个工具就裸奔到 AI 面前，风险由用户承担。但对信任的上游，逐个人工
-- 点启用是纯粹的重复劳动——于是把这件事变成 per-server 策略：开了的
-- server，探活新发现的工具直接启用，并写一条变更记录（可见、可回溯）。
--
-- 默认 0（关）：不开就等于现状，已存的 server 行为完全不变。
ALTER TABLE servers ADD COLUMN auto_enable INTEGER NOT NULL DEFAULT 0;
