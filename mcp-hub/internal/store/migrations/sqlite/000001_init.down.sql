-- 回滚整个基线：按依赖倒序删表。
-- 外键已开启，所以顺序不能随意调换（先删被引用的表会撞约束）。
DROP TABLE IF EXISTS tool_embeddings;
DROP TABLE IF EXISTS tool_changes;
DROP TABLE IF EXISTS call_logs;
DROP TABLE IF EXISTS credentials;
DROP TABLE IF EXISTS tools;
DROP TABLE IF EXISTS categories;
DROP TABLE IF EXISTS api_keys;
DROP TABLE IF EXISTS servers;
