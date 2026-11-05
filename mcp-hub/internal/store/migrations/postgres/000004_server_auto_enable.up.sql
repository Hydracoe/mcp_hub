-- 新工具自动启用开关（O5）。
-- 默认安全语义是"上游新增的工具先人工看过再放行"（T1）；对信任的上游可改成
-- per-server 策略：探活新发现的工具直接启用，并写一条变更记录（可见、可回溯）。
-- 默认 0（关），已存 server 行为完全不变。
ALTER TABLE servers ADD COLUMN auto_enable INTEGER NOT NULL DEFAULT 0;
