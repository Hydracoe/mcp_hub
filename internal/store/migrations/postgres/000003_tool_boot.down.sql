-- 回滚到过滤名单模型。引导名单里未消费的条目无法还原成
-- allow 名单（语义不同），回滚前请确认没有待消费的引导名单。
ALTER TABLE servers DROP COLUMN boot_enabled_tools;
ALTER TABLE servers ADD COLUMN tool_filter_mode TEXT NOT NULL DEFAULT 'allow_all';
ALTER TABLE servers ADD COLUMN tool_filter_list TEXT NOT NULL DEFAULT '[]';
