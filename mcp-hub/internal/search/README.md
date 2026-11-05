# search — mcp-hub 检索基座

纯算法检索模块：不依赖 store 与 hub，不知道数据库表和 HTTP 路由。它提供 BM25 / 向量两路检索的原子能力以及融合、精排配套件；取数、建索引与流程编排在 [internal/hub/retrieval](../hub/retrieval)。

## 包结构

| 子包 | 内容 | 依赖 |
| --- | --- | --- |
| `corpus/` | 语料组装：字段开关（`Fields`）、文本拼装（`Text`）、增量指纹（`Hash`）、参数线索（`SchemaHints`）、代编码（`SpecString`） | 无 |
| `lexical/` | 词法检索：中英混合分词（`Tokenize`/`Dedupe`）、BM25 倒排索引与打分（`BM25`） | `rank` |
| `rank/` | 排序融合原语：`Hit`/`SortHits`/`TopK`、RRF、余弦相似度（`Cosine`/`Normalize`/`Dot`） | 无 |
| `client/` | 外部模型客户端：`/v1/embeddings`（`Embedder`）与 `/v1/rerank`（`Reranker`），含默认服务与模型常量 | 无 |

依赖方向只有 `lexical → rank`，其余是叶子包。

## 检索流水线

```
query ─┬─ bm25：词法相关度（内存倒排） → lexical
       └─ vector：向量化 → 余弦近邻（PG pgvector / SQLite 内存） → client + store
              ↓ RRF 融合（只看名次） → rerank 精排（挂了保持顺序） → 过滤（分类/server）→ 薄结果
```

降级：rerank 挂 → 保持 RRF 顺序；向量一路挂（embed 未配置 / 当前代未就绪 / 检索报错）→ 退回纯 bm25。

## 设计要点

1. **中英混合分词。** 中文按 unigram + bigram 双份切（"搜索引擎" → 搜/索/引/擎 + 搜索/索引/引擎）：unigram 保召回，bigram 让两字词精确命中。ASCII 连续段整体小写保留（GitHub → github）。
2. **语料只含工具自身、创建后不变的字段。** 工具名 / 描述 / 参数线索 / 标签 / server 描述可进语料；分类等可变元数据排除，避免改分类触发重嵌（分类仍参与词法检索与过滤）。
3. **确定性优先。** 语料段序固定、schema 键序排序、同分按 ID 排序；同一工具每次组装出的语料逐字节一致，否则语料哈希逐轮变化，增量重嵌退化为全量重嵌。
4. **无常驻索引。** 语料规模数百到数千工具，BM25 索引随聚合快照现场重建即可，远低于延迟预算。
5. **错误不静默。** `Cosine` 对维度不一致、零向量报错；`SchemaHints` 区分"JSON 非法"（error）与"没有参数"（空串）；`NewEmbedder` 要求 `dims > 0`，跳过校验必须显式用 `NewEmbedderUnchecked`；`BM25.Search` 的 `topK`、`Reranker.Rerank` 的 `topN`、`RRF` 的 `k` 非正数时返回空，不换成默认值。

## 关键概念

- **代（generation）**：`(model, dims, spec)` 是向量身份（`corpus.SpecString`）。换模型 / 维度 / 语料字段即新代；新代整代建好后才原子激活（`activeGen`），切换期间用上一代；旧代行在就绪后清除，没有回滚接口。
- **语料哈希 `corpus.Hash`**：增量重嵌的判定依据，只含语料文本，不含 model/dims。
- **维度校验**：配置 dimensions 与模型实际输出不一致会导致检索静默降级；`client.Embedder.Embed` 在写入前逐条校验。
- **`SchemaHints` / BM25**：从 inputSchema 摘"参数名 + 一句话说明"（type/required 是噪声）；索引是"词元 → posting"，取 topK 走最小堆（`rank.TopK`）。向量比较可先 `Normalize` 再 `Dot`；实际向量检索走 store，不在这条路径上。

## 测试

运行 `go test ./internal/search/...`。纯算法包，全部为不依赖外部服务的白盒测试；真实 embed/rerank/pgvector 的集成行为由 `internal/hub` 与 `internal/store/tests` 覆盖。
