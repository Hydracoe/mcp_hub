-- 000007_vector_type
-- tool_embeddings 的 vector 列从 TEXT（JSON 数组）切换为 pgvector 的 vector 类型。
--
-- 从此扩展是 PostgreSQL 部署的硬依赖：缺了它这条迁移会失败，进程起不来。
-- SQLite 没有向量类型，仍走 TEXT + Go 侧内存余弦（见 migrations/sqlite）。
CREATE EXTENSION IF NOT EXISTS vector;

-- 用不带维度的 vector（pgvector >= 0.7.0）：主键允许多代并存，而不同代的
-- 维度可以不同（换模型即换维度），定长列会拒收第二个维度的行。
-- 已有数据是 "[1,2,3]" 形态的文本，vector 的输入函数直接解析。
ALTER TABLE tool_embeddings ALTER COLUMN "vector" TYPE vector USING "vector"::vector;

-- 代身份索引：检索条件恒为 (model, dims, spec)，把"这一代有哪几行"收窄成
-- 一次索引扫描。
--
-- 刻意**不建**向量索引（HNSW / IVFFlat）：ANN 索引在建索引时就锁死单一
-- 维度，与"多代混合维度并存"的热切换要求直接冲突。工具量级（数千行）下
-- 精确全表扫描是毫秒级，结果还比近似近邻更准。
CREATE INDEX idx_tool_embeddings_gen ON tool_embeddings (model, dims, spec);
