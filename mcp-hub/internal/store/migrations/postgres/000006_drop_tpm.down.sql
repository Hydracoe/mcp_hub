-- 回滚 TPM 移除：把两列加回来。只恢复表结构，不恢复数据——
-- 被删掉的限额与估算值无法还原，一律按"未配置"（0）重新开始。
ALTER TABLE credentials ADD COLUMN IF NOT EXISTS tpm_limit INTEGER NOT NULL DEFAULT 0;
ALTER TABLE servers ADD COLUMN IF NOT EXISTS est_tokens_per_call INTEGER NOT NULL DEFAULT 0;
