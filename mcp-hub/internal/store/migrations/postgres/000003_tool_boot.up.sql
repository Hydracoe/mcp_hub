-- 工具管控归一：删掉 server 级的 allow/block 过滤名单，工具启停只由 tools.enabled 决定。
-- boot_enabled_tools 是创建时的一次性引导名单：首次探活把名单内工具写成 enabled=1，随后清空；
-- 此后启停只走 /tools/:name/enable|disable。本迁移会 DROP 过滤名单列，回滚只能重建默认值。
ALTER TABLE servers DROP COLUMN tool_filter_mode;
ALTER TABLE servers DROP COLUMN tool_filter_list;
ALTER TABLE servers ADD COLUMN boot_enabled_tools TEXT NOT NULL DEFAULT '[]';
