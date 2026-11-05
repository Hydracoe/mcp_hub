-- 与 PG 侧 000007 同名的代身份索引：检索条件恒为 (model, dims, spec)。
-- SQLite 没有向量类型，vector 列保持 TEXT（JSON 数组），检索走 Go 侧内存余弦。
CREATE INDEX idx_tool_embeddings_gen ON tool_embeddings (model, dims, spec);
