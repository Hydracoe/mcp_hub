-- 回滚到"冷却只在内存里"：重启即忘。
ALTER TABLE credentials DROP COLUMN cooling_until;
