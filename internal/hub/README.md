# hub — mcp-hub 聚合核心

mcp-hub 的中枢包：把一批下游 MCP server 的工具聚合成**一个**对外 MCP 端点，并负责连接的生死、工具列表的刷新、自然语言检索与密钥池的调度。上游是外部 AI 客户端，下游是第三方 MCP server。

它消费 [internal/downstream](../../internal/downstream) 的连接与探活能力、[internal/store](../../internal/store) 的持久层、[internal/search](../../internal/search) 的检索原语，自己只做编排。

## 包结构

`internal/hub` 本身是门面包：`Service` 在这里装配四块能力，管理面（配置装载、密钥池、分类、调用日志、meta-tool、接入鉴权）也在这里。四块能力各自成包——同一领域的代码在同一个文件夹里。

```
internal/hub/            门面：Service 编排 + meta-tool + 接入 key 校验
├── registry/            对外聚合视图：工具列表 + 路由表（不碰连接与存储）
├── pool/                下游连接池：懒建连、失败退避、密钥池、优雅退场
├── callctx/             一次调用的上下文：谁调的 + 用了哪把凭据（叶子包）
├── aggregate/           对外 MCP 端点：tools/list 聚合 + tools/call 路由
├── retrieval/           检索与向量：内存索引、三路检索、按代重建、自动归类
└── tests/               黑盒集成测试（package tests）
```

| 包           | 文件                      | 内容                                                                                              |
| ------------ | ------------------------- | ------------------------------------------------------------------------------------------------- |
| `hub`        | `service.go`              | `Service` 本体：常量、构造、访问器、`sources` 取数、`SearchTools` 转发、`Ready` / `Close`           |
|              | `service_reload.go`       | `Reload` 与密钥环同步、密钥事件落库                                                                 |
|              | `service_refresh.go`      | 探活刷新（`RefreshAll` / `ProbeServer`）、`ApplyServer`、创建引导名单                                   |
|              | `service_credentials.go`  | 密钥池增删改查、单密钥探活、用量摘要                                                                |
|              | `service_catalog.go`      | 分类 CRUD、server 列表与启停、缓存失效                                                              |
|              | `service_logs.go`         | 异步日志队列与落库 worker                                                                           |
|              | `meta_tools.go`           | 5 个 `hub__*` meta-tool：`search_tools` / `list_categories` / `list_servers` / `list_tools` / `tool` |
|              | `apikey.go`               | 接入 key 校验与 `last_used_at` 节流（ctx 注入见 `callctx`）                                          |
|              | `errors.go`               | 包级错误与 `IsNotFound`                                                                             |
| `registry`   | `registry.go`             | 路由表与不可变聚合快照（`Snapshot`）；对外名 `ExposedToolName` = `<serverKey>__<toolName>` |
| `pool`       | `pool.go`                 | 连接池本体：懒建连、配置指纹、`Sync` / `Drop` / `Close`                                             |
|              | `call.go`                 | 调用路径：`Get` / `CallTool` / `ListTools` 与 `runWithKeys` 的调用内重试                            |
|              | `keyring.go`              | 每 server 的密钥环：同步、查询、尝试预算、密钥事件上报                                              |
|              | `drain.go`                | 优雅退场：`Retire` 摘出连接、等在飞调用、超时强关                                                   |
|              | `health.go`               | 失败退避：连续失败计数与退避窗口                                                                    |
| `callctx`    | `callctx.go`              | `WithCaller` / `CallerID`（谁调的）与 `WithMeta` / `Meta.Record`（用了哪把凭据、第几次尝试）        |
| `aggregate`  | `aggregate.go`            | 对外 MCP server：`tools/list` 聚合、`tools/call` 路由、meta 列表模式、`serverInstructions`          |
|              | `calllog.go`              | `LogRecord` 调用日志结构、失败建议文案 `adviceFor`                                                  |
| `retrieval`  | `engine.go`               | `Engine`：持有索引与嵌入管理器，对外提供 `Search` / `RebuildIndex`                                  |
|              | `search.go`               | 三路检索编排（RRF + rerank）、结果与 facet 装配、分类解析                                           |
|              | `index.go`                | 内存索引与语料：`collectToolDocs`、分词、索引重建                                                   |
|              | `embed.go`                | embedding 配置与 `EmbedManager` 本体                                                               |
|              | `rebuild.go`              | 按"代"的增量向量重建与启动对账（`Reconcile`）                                                       |
|              | `category.go`             | 分类向量刷新与自动归类                                                                              |
| `tests`      | 见文末                    | 黑盒集成测试（`package tests`）                                                                     |

依赖方向是单向的，没有环：

```
hub ──► aggregate ──► registry, pool, callctx
 │          │
 │          └──► retrieval ──► registry
 └─────────────────────────► (同上)
```

子包都不认识 `Service`：`aggregate` 通过 `Deps` 里的函数字段（`Sources` / `SpecOf` / `Log`）拿状态，`pool` 通过 `OnKeyEvent` 回调把密钥变化交回门面，`retrieval` 只吃 `*store.Store`。这样"编排"与"能力"可以分别测试，也让 `Service` 不必把内部状态暴露成公开 API。

## 业务背景

一个 AI 客户端只想面对一个端点。hub 做的事是把 N 个下游的工具拼成一张工具表，把调用按名字路由回去。

```
        AI 客户端
            │  POST /mcp
            ▼
   aggregator（聚合 MCP server）
     ├─ tools/list ──► Registry：当前聚合快照（工具列表 + 路由表）
     └─ tools/call ──► 按对外名查路由 → Pool → 下游连接
                            ▲
   service（编排）───────────┘
     ├─ Reload：读库 → Pool.Sync（连接对齐）→ Aggregator.Refresh → Registry.Build
     ├─ RefreshAll：并发探活 → 落库 → 重建快照 + 重建检索索引 + 增量 embed
     └─ 日志：异步队列（512）批量落库，队列满则丢弃（观测不该成为可用性依赖）
```

三条要点：

- **工具列表是"最近一次已知"而不是"此刻真实"。** 探活失败**不清空**该 server 的工具缓存，只更新状态。下游抖动不该让 AI 手里的工具凭空消失——缓存的移除只有两条路径：重新探活成功整体替换，或该 server 被删除。
- **调用失败返回 `isError` 结果，不返回 JSON-RPC 错误。** 下游故障的含义是"这次调用没成功"，不是"端点坏了"；返回协议错误会让客户端判定会话异常，而 `isError` 让 AI 能读到原因并自行决策。失败文案由 `adviceFor` 按错误分类给出**下一步建议**（"稍后重试"还是"去处理密钥"），给错建议比不给更糟。
- **聚合快照整体替换，读路径无锁。** `Registry` 持 `atomic.Pointer[Snapshot]`，`Build` 造一份新的原子换上；`tools/list` 走内存快照，一次库都不查——这是 P50 < 1ms 的来源。

## 设计思路

七条核心决定，读代码前先记住它们：

1. **对外名两段式，路由走显式映射表。** 名字只有 `serverKey` 与下游工具名两段，两者都是创建后不再变的字段，所以名字天然稳定。路由靠 `routes` 表查（`exposedName → serverID + toolName`），**绝不切分字符串反推**——下游工具名本身可能含 `__`，切分方案会在这种边界上静默串线。
2. **分类不进工具名。** 分类是可变的组织元数据，写进标识符就等于"改一次分类 = 对所有调用方做一次破坏性重命名"。它仍可从 `hub__list_categories`、检索 facet 与过滤器拿到，不进名字不损失能力。
3. **工具列表按指纹比较，没变就不动 MCP server。** `SetTools` 会向所有已连客户端广播 `tools/list_changed`；周期探活每轮都抖一次列表，会让 AI 不断重新规划。指纹刻意不含 server 可用性——抖动只影响调用结果，不影响工具定义。
4. **Registry 决定"发给谁"，Pool 决定"怎么发"。** Pool 是唯一持有下游连接的地方：常驻连接、懒建连（`dialMu` 保证并发首次连接只握手一次）、失败退避（2s 起指数退避至 30s），并在退避窗口内快速失败而不发网络请求。
5. **连接是否重建，只看 `ConnFingerprint`。** 换密钥、改显示名都**不动连接**；只有 url / command / env / 超时 / 协议版本变了才重建。拆连接会打断正在跑的调用，而用户点"改个名字"时完全没有这个预期。这是"改个名字怎么把别人的任务弄挂了"这类问题的根治点。
6. **停用与改配置都是两阶段优雅退场。** 停用先让工具**立即**从聚合列表撤下（AI 从此不会再派新调用），再让连接进入收尾（拒绝新调用、在飞的跑完），在飞归零后关闭。等待上限 1 分钟（`DefaultDrainTimeout`），超时强制关闭并把仍在跑的调用标记为 `CANCELED`——没有上限，一个卡死的下游会让该 server 永远停在"停用中"。`Drop` 是唯一的例外路径，只在进程退出或连接已确证损坏时用。
7. **密钥池是可选增强，不是新的必经路径。** 未配密钥 → 单密钥模式（一直用 `servers.headers`，一次调用一次机会）；配了密钥 → 轮询换钥，密钥级失败（401/402/403/429）**换一把透明重试**，调用方无感。超时与 5xx **绝不重试**：MCP 工具调用没有标准幂等键，盲目重试可能让有副作用的工具执行两次。所有密钥同时不可用时才以带 `retryAfter` 的 `ErrNoUsableKey` 上抛。

检索一侧另有三条：

8. **检索索引与 embed 语料共用 `collectToolDocs`。** 同一个函数决定"哪些工具可被搜到"，两边口径不一致会让增量重建退化为全量重嵌，检索也会返回 AI 实际调不到的工具。启用语义与聚合路径完全一致：只有 `tools.enabled` 的工具可被搜到。
9. **工具启停只有一个开关：`tools.enabled`。** 聚合端点、检索、管理界面读同一份事实，不存在第二层名单；创建向导的勾选通过一次性引导名单 `boot_enabled_tools` 在首次探活时兑现。
10. **降级阶梯，每一级都保持可用。** rerank 挂 → 保持 RRF 融合顺序；embed 挂 → 只用 bm25；无 pgvector → keyword 与 bm25 融合。辅助能力挂掉不该让检索整体报错——搜不到比报错更接近可用。

## 关键概念

- **meta 列表模式（`MCPHUB_LIST_MODE`）**：`meta` 只向客户端列出 5 个 `hub__*` 工具，下游工具靠搜索发现，列表小、AI 规划轮次少；`all` 列出全部聚合工具。两种模式下 `tools/call` 行为完全一致——`metaOnlyFilter` 靠 `len(tools) == 1` 识别 call 路径并一律放行，因为 mcp-go 的 list 与 call 共用同一条过滤器链。
- **树式发现**：`hub__search_tools` 默认返回"薄结果"（不带 `inputSchema`）+ 末尾 `facets`（命中分布），AI 据此收窄到 3 个以内，再用 `hub__tool` 取完整 schema 调用。单次搜索的上下文开销与工具总数无关。
- **两轴判决**：`ErrorKind` 回答"下游为什么失败"（面向人），`KeyVerdict` 回答"这把密钥还能不能用"（面向调度器）。同一个 5xx，前者是 `UPSTREAM_ERROR`，后者是 `NEUTRAL`（别动密钥）——合并成一个枚举会让两边加值都污染对方的语义。
- **代（generation）**：`(model, dims, spec)` 三元组是一代向量的身份。换模型 / 维度 / 语料字段即新代；进程启动时 `Reconcile` 把当前代补齐，补全后才清掉其它代——embed 服务暂时打不通时，旧代的行是唯一的退路，没把握时不该删。
- **调用日志的状态取值**：`ok` / `error` 是调用结果；`key` 是密钥事件（冷却 / 弃用 / 恢复），单列是为了让"按 status=error 查失败"时不把事件混进来，同时又能专门回答"这把密钥为什么不转了"。

## 测试

测试主体集中在 `tests/` 目录（`package tests`），黑盒驱动 hub 的导出 API；少数只能碰未导出符号的白盒用例（`registry` 的 schema 兜底、`aggregate` 的 meta 前缀过滤）留在各自子包目录内：

```sh
go test ./internal/hub/tests/ -count=1
go test ./internal/hub/... -count=1   # 等价，含包内
```

放在独立目录而不是留在包内，是为了让测试与实现的文件划分解耦——hub 内部怎么拆文件都不用改测试。代价是只能碰导出 API，内部细节（如 `registry.ApplyToolFilter`）通过行为用例间接覆盖。

| 文件                  | 覆盖                                                                             |
| --------------------- | -------------------------------------------------------------------------------- |
| `aggregate_test.go`   | 聚合与路由主链路、schema/入参/结果透传、故障隔离、工具缓存保留、创建引导名单、并发调用     |
| `graceful_test.go`    | 凭据轮换与在飞调用、停用收尾与超时强关、连接重建判据（`ConnFingerprint`）           |
| `keypool_e2e_test.go` | 密钥池端到端：限流换钥、废弃坏钥、池子打空、并发归因、单密钥模式不回归             |
| `m7_test.go`          | meta 列表模式与 5 个 meta-tool、三路检索与 facet、向量增量重建与对账、自动归类      |
| `cache_test.go`       | 管理读路径的缓存命中与写路径失效范围                                               |
| `apikey_test.go`      | 接入 key 鉴权与调用日志归因                                                        |
| `service_test.go`     | `RefreshAll` 对停用 server 的排除语义                                             |

需要一台可达的 PostgreSQL（`MCP_HUB_TEST_DSN`，或仓库根目录的 `docker compose up -d`）；每个测试建一个随机命名的独立库。并发相关的改动请加 `-race`。
