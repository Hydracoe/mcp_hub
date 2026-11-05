-- 工具管控归一：删掉 server 级的 allow/block 过滤名单，
-- 工具的启停只由 tools.enabled 一个开关决定。
--
-- 原来的设计里"创建向导勾选哪些工具"借道 allow 名单传给首次探活，
-- 名单随后又作为持续性过滤存在，与逐工具开关叠成两层真相——
-- "已启用却不生效（被名单过滤）"的状态让人困惑，且名单维护本身
-- 就是逐个手动操作，没有独立价值。
--
-- 替代通道：boot_enabled_tools 是创建时的一次性引导名单，首次探活
-- 落库时名单内的工具行直接 enabled=1，随后清空；此后启停只走
-- /tools/:name/enable|disable。
ALTER TABLE servers DROP COLUMN tool_filter_mode;
ALTER TABLE servers DROP COLUMN tool_filter_list;
ALTER TABLE servers ADD COLUMN boot_enabled_tools TEXT NOT NULL DEFAULT '[]';
