# search — mcp-hub 检索基座

mcp-hub 的纯算法检索模块：不依赖 store 与 hub，不知道数据库表和 HTTP 路由的存在。它提供三路检索的原子能力（keyword / BM25 / 向量）以及融合、精排的配套件；由 [internal/hub/retrieval](../hub/retrieval) 负责取数、建索引和流程编排。

## 包结构

| 子包       | 内容                                                                                                                      | 依赖   |
| ---------- | ------------------------------------------------------------------------------------------------------------------------- | ------ |
| `corpus/`  | 语料组装：字段开关（`Fields`）、文本拼装（`Text`）、增量指纹（`Hash`）、参数线索（`SchemaHints`）、代编码（`SpecString`） | 无     |
| `lexical/` | 词法检索：中英混合分词（`Tokenize`/`Dedupe`）、BM25 倒排索引与打分（`BM25`）、keyword 保序命中（`BM25.KeywordHits`）        | `rank` |
| `rank/`    | 排序融合原语：`Hit`/`SortHits`/`TopK`、RRF 融合、余弦相似度（`Cosine`/`Normalize`/`Dot`）                                  | 无     |
| `client/`  | 外部模型客户端：`/v1/embeddings`（`Embedder`）与 `/v1/rerank`（`Reranker`），含默认服务与模型常量                         | 无     |

依赖方向只有 `lexical → rank`（BM25 的打分结果用 `rank.Hit` 承载），其余全是叶子包。

## 业务背景：AI 怎么找工具

hub 把一批 MCP server 的工具聚合成统一工具池。AI 客户端只会说人话（"帮我查北京天气"），不会报工具名，所以 hub 必须提供自然语言 → 工具的检索（`hub__search`）。

默认 hybrid 模式的流水线：

```
             ┌─ keyword：词元命中 / 子串命中（保序、不打分，零成本兜底）   → lexical
 query ──────┼─ bm25：词法相关度打分（内存倒排索引）                      → lexical
             └─ vector：query 向量化 → pgvector 余弦距离近邻              → client + store
                     ↓
        RRF 融合（只看名次，不吃分数——BM25 分数与余弦距离量纲不同，不能混加） → rank
                     ↓
        rerank 精排（外部模型；挂了就保持融合顺序）                        → client
                     ↓
        过滤（分类 / server / tag）→ 薄结果（默认不带 schema，AI 圈定候选后再按名取全量）
```

降级阶梯，每一级都保持可用：rerank 挂 → 保持融合顺序；embed 挂 → 只用 bm25；未装 pgvector → keyword+bm25 融合。辅助能力挂掉不让检索整体报错——搜不到比报错更接近可用。

## 设计思路

读代码前先记住四条决定：

1. **中英混合分词。** 中文没有词边界，按 unigram + bigram 双份切（"搜索引擎" → 搜/索/引/擎 + 搜索/索引/引擎）：unigram 保证召回，bigram 让"搜索""索引"这类两字词精确命中而非凑巧拼出。ASCII 连续段整体小写保留（GitHub → github）。

2. **语料（embedding 的输入文本）只含工具自身、创建后不变的字段。** 向量化要花 API 钱，语料口径直接决定成本。工具名 / 描述 / 参数线索 / 标签 / server 描述可进语料；分类等可变元数据刻意排除——改一次分类名不该触发一批重嵌。分类仍参与词法检索与过滤，那条路是纯内存重建，零 API 成本。

3. **确定性优先。** 语料段序固定、schema 键序排序、同分按 ID 排序。同一工具每次组装出的语料必须逐字节一致，否则语料哈希逐轮变化，增量重嵌退化为全量重嵌（每轮探活都白调一遍 embedding API）。

4. **无常驻索引。** 语料规模小（数百到数千工具），BM25 索引随聚合快照现场重建即可，开销远低于延迟预算，无需增量维护。

5. **错误不静默。** 本模块只接受合法入参、如实返回错误，不替调用方挑默认值，也不把"算不出来"伪装成一个看起来正常的结果：
   - `Cosine` 对维度不一致、零向量报错——返回 0 会让维度配错时的检索与自动分类**无声失效**；
   - `SchemaHints` 区分"JSON 非法"（返回 error）与"工具没有参数"（返回空串）；
   - `NewEmbedder` 要求 `dims > 0`，没有"传 0 表示不校验"的旁路，要跳过校验走显式的 `NewEmbedderUnchecked`；
   - `BM25.Search` 的 `topK`、`Reranker.Rerank` 的 `topN`、`RRF` 的 `k` 收到非正数时按字面返回空，而不是悄悄换成某个默认值。

## 关键概念

- **代（generation）**：`(model, dims, spec)` 三元组是一代向量的身份（`corpus.SpecString`）。换模型、换维度或换语料字段即新代——新代整代建好再原子切换，切换期间新旧并存，回滚靠保留上一代。
- **语料哈希 `corpus.Hash`**：增量重嵌的判定依据——语料文本没变就不重嵌。只含文本本身，不含 model/dims：换模型是"整代"的事，由代身份表达，逐条比对帮不上忙。
- **维度校验**：配置 dimensions 与模型实际输出不一致是唯一静默坏掉的坑（不报错，检索悄悄降级、日志仅一行 warn）。`client.Embedder.Embed` 在写入前逐条校验，坏数据进不了库；`NewEmbedder` 要求 dimensions 为正，没有"传 0 表示不校验"的旁路——需要跳过校验的场合走 `NewEmbedderUnchecked`，让"我关掉了校验"在调用点一眼可见。
- **`corpus.SchemaHints`**：从 inputSchema 摘"参数名 + 一句话说明"。参数名是强信号（搜"按经纬度查"，描述里未必有"经纬度"，参数名 geocode/lng 会有）；整段 JSON schema 的 type/required 是噪声，塞进去只会冲淡语义。解析失败返回 error（下游数据质量问题，由调用方计数上报），"没有参数"返回空串——两者不能混为一谈。超长时按完整的"键名（说明）"单元截断，不留半个括号。
- **BM25 倒排**：索引是"词元 → posting 列表"，打分只遍历真正含该词元的文档；取 topK 走最小堆（`rank.TopK`），不对全部命中排序。
- **向量相似度**：`rank.Cosine` 是单次比较的入口；一批候选与同一查询比较时，用 `Normalize` 把两边都变成单位向量，之后 `Dot` 即可，省掉每次重算范数。向量检索本身走 pgvector，不在这条路径上。

## 测试

```sh
go test ./internal/search/...
```

纯算法包，全部为不依赖外部服务的白盒测试。涉及真实 embed/rerank/pgvector 的集成行为由 internal/hub 与 internal/store/tests 覆盖。
