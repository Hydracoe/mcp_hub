-- 回滚后全部 server 回到"新工具默认禁用、须人工启用"。
ALTER TABLE servers DROP COLUMN auto_enable;
