-- 移除 TPM（每分钟 token 数）限流：整条业务下线，只保留 RPM。
-- MCP 规范未约定响应里的 token 用量，TPM 只能靠手填的 per-server 估算常量换算，起不到额外保护。
-- 两列一并删，避免留下"配了但没有计量来源"的半残状态；本迁移会丢失已配置的限额数据。
ALTER TABLE credentials DROP COLUMN IF EXISTS tpm_limit;
ALTER TABLE servers DROP COLUMN IF EXISTS est_tokens_per_call;
