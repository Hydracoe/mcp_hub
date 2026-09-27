-- 移除 TPM（每分钟 token 数）限流：整条业务下线，只保留 RPM。
--
-- 下线理由：MCP 规范未约定响应里的 token 用量，绝大多数下游根本不返回，
-- TPM 唯一的计量来源只能是用户手填的 per-server 估算常量
-- （servers.est_tokens_per_call）。结果就是"用估算单价把每分钟请求数换算成
-- token 数"——把确定量换成了估计量，却没有换来任何额外保护：
--   * 配额上限（credentials.tpm_limit）与实际计量常常对不上，界面还会显示
--     虚假的 0/100k，看起来一切正常；
--   * 单配上限不配估算值时该 quota 恒定放行，是"配了却完全不起作用"的死门。
-- 网关真正挡得住的是 RPM：一次调用就是一次请求，没有含糊。
--
-- 两列一并删：只删其中一列会留下一个"配了但没有计量来源"的半残状态。
ALTER TABLE credentials DROP COLUMN IF EXISTS tpm_limit;
ALTER TABLE servers DROP COLUMN IF EXISTS est_tokens_per_call;
