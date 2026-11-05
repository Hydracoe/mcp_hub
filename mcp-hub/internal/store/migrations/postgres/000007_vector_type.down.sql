-- vector::text 输出 "[1,2,3]"，与 000001 的 TEXT 存储形态（JSON 数组）一致。
-- 不 DROP EXTENSION：库里其它地方可能还在用，且回滚目标只是恢复本表的列类型。
DROP INDEX IF EXISTS idx_tool_embeddings_gen;
ALTER TABLE tool_embeddings ALTER COLUMN "vector" TYPE text USING "vector"::text;
