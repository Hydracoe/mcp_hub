# hub — mcp-hub 聚合核心

mcp-hub 的中枢包：把一批下游 MCP server 的工具聚合成**一个**对外 MCP 端点，负责连接生死、工具列表刷新、自然语言检索与密钥池调度。它消费 [internal/downstream](../../internal/downstream) 的连接与探活、[internal/store](../../internal/store) 的持久层、[internal/search](../../internal/search) 的检索原语，自己只做编排。

## 包结构

```
internal/hub/            门面：Service 编排 + meta-tool + 接入 key 校验
├── registry/            对外聚合视图：工具列表 + 路由表（不碰连接与存储）
├── pool/                下游连接池：懒建连、失败退避、密钥池、优雅退场
├── callctx/             一次调用的上下文：谁调的 + 用了哪把凭据（叶子包）
├── aggregate/           对外 MCP 端点：tools/list 聚合 + tools/call 路由
├── retrieval/           检索与向量：内存索引、两路检索、按代重建、自动归类
└── tests/               黑盒集成测试（package tests）
```

| 包 | 文件与内容 |
| --- | --- |
| `hub` | `service.go`（`Service` 本体、`sources`、`SearchTools`、`Ready`/`Close`）；`service_reload.go`（`Reload`、密钥环同步）；`service_refresh.go`（`RefreshAll`/`ProbeServer`、`ApplyServer`）；`service_credentials.go`（密钥池 CRUD、单密钥探活、用量摘要）；`service_catalog.go`（分类 CRUD、server 启停、缓存失效）；`service_logs.go`（异步日志队列与落库）；`meta_tools.go`（5 个 `hub__*`）；`apikey.go`（接入 key 校验与 `last_used_at` 节流）；`errors.go`（包级错误与 `IsNotFound`） |
| `registry` | `registry.go`：路由表与不可变聚合快照（`Snapshot`）；对外名 `ExposedToolName` = `<serverKey>__<toolName>` |
| `pool` | `pool.go`（懒建连、配置指纹、`Sync`/`Drop`/`Close`）；`call.go`（`Get`/`CallTool`/`ListTools`、`runWithKeys` 调用内重试）；`keyring.go`（每 server 密钥环、尝试预算、密钥事件）；`drain.go`（`Retire`、等在飞调用、超时强关）；`health.go`（连续失败计数与退避窗口） |
| `callctx` | `callctx.go`：`WithCaller`/`CallerID` 与 `WithMeta`/`Meta.Record`（凭据与尝试次数） |
| `aggregate` | `aggregate.go`（`tools/list` 聚合、`tools/call` 路由、meta 列表模式、`serverInstructions`）；`calllog.go`（`LogRecord`、失败建议 `adviceFor`） |
| `retrieval` | `engine.go`（`Engine`、`Search`/`RebuildIndex`）；`search.go`（两路检索编排、RRF + rerank、facet 与分类解析）；`index.go`（内存索引与语料、`collectToolDocs`、分词）；`embed.go`（embedding 配置与 `EmbedManager`）；`rebuild.go`（按代增量重建与启动对账 `Reconcile`）；`category.go`（分类向量刷新与自动归类） |
| `tests` | 黑盒集成测试（`package tests`），见文末 |

依赖方向单向、无环：`aggregate → registry, pool, callctx, retrieval`，`retrieval → registry`。子包都不认识 `Service`：`aggregate` 通过 `Deps` 的函数字段（`Sources`/`SpecOf`/`Log`）拿状态，`pool` 通过 `OnKeyEvent` 回调交回密钥变化，`retrieval` 只吃 `*store.Store`。

## 设计要点

1. **对外名两段式，路由走显式映射表。** 名字只含 `serverKey` 与下游工具名，路由靠 `exposedName → serverID + toolName` 的 `routes` 表查询，**绝不切分字符串反推**（下游工具名可能含 `__`）。
2. **分类不进工具名。** 分类是可变的组织元数据，改分类等于对所有调用方做破坏性重命名；仍可从 `hub__list_categories`、检索 facet 与过滤器拿到。
3. **工具列表按指纹比较，聚合快照整体替换。** 列表没变就不广播 `tools/list_changed`，避免 AI 反复重新规划；指纹不含 server 可用性。`Registry` 持 `atomic.Pointer[Snapshot]`，读路径无锁。连接是否重建只看 `ConnFingerprint`（url / command / env / 超时 / 协议版本），换密钥、改显示名不动连接。
4. **停用与改配置都是两阶段优雅退场。** 先让工具从聚合列表立即撤下，再拒绝新调用、等在飞调用跑完；上限 `DefaultDrainTimeout`（1 分钟），超时强关并把仍在跑的调用标记为 `CANCELED`。`Drop` 只在进程退出或连接已确证损坏时用。Pool 常驻连接、懒建连（`dialMu` 保证并发首连只握手一次）、失败退避 2s 起指数至 30s，退避窗口内快速失败不发网络请求。
5. **密钥池是可选增强。** 未配密钥 → 单密钥模式（一直用 `servers.headers`）；配了 → 轮询换钥，401/402/403/429 **换一把透明重试一次**。超时与 5xx **绝不重试**（MCP 工具调用没有标准幂等键，重试可能让有副作用的工具执行两次）；所有密钥不可用时抛带 `retryAfter` 的 `ErrNoUsableKey`。
6. **降级阶梯每级都可用。** rerank 挂 → 保持 RRF 融合顺序；向量一路挂（embed 未配置 / 当前代未就绪 / 检索报错）→ 退回纯 bm25。
7. **工具启停只有一个开关 `tools.enabled`。** 聚合端点、检索、管理界面读同一份事实；检索索引与 embed 语料共用 `collectToolDocs`，口径不一致会让增量重建退化为全量重嵌。

## 关键概念

- **meta 列表模式（`MCPHUB_LIST_MODE`）**：`meta` 只列 5 个 `hub__*`，`all` 列全部聚合工具；两种模式下 `tools/call` 行为一致。元工具名单由 `SetMetaTools` 登记，占用 `hub__*` 保留命名空间。
- **树式发现**：`hub__search_tools` 默认返回薄结果（不带 `inputSchema`）+ `facets`，AI 收窄后再用 `hub__tool` 取完整 schema。
- **两轴判决**：`ErrorKind` 回答"下游为什么失败"（面向人），`KeyVerdict` 回答"密钥还能不能用"（面向调度器）；同一个 5xx 是 `UPSTREAM_ERROR` 与 `NEUTRAL`。
- **代（generation）**：`(model, dims, spec)` 是一代向量的身份；检索只查最近一次完全就绪的 `activeGen`，半成品代不参与。启动时 `Reconcile` 补齐当前代后才清掉其它代。
- **调用日志状态**：`ok` / `error` 是调用结果；`key` 是密钥事件（冷却 / 弃用 / 恢复），单列以免混进失败查询。

## 测试

黑盒测试在 `tests/`（`package tests`），覆盖聚合与路由、故障隔离、优雅退场、密钥池端到端、meta 模式与检索、缓存、接入 key 鉴权。少数白盒用例留在 `internal/hub/`（连败拦截状态机、凭据试测并发上限）。

```sh
go test ./internal/hub/tests/ -count=1
go test ./internal/hub/... -count=1   # 含包内用例
```

需要一台可达的 PostgreSQL（`MCP_HUB_TEST_DSN`，或仓库根 `docker compose up -d`）；每个测试建一个随机命名的独立库。并发相关改动请加 `-race`。
