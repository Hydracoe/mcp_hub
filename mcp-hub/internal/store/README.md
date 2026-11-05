# store — mcp-hub 持久层

mcp-hub 唯一的持久化包。数据落在 PostgreSQL（pgvector 扩展支撑 `tool_embeddings` 的向量列；SQLite 无向量类型，检索走内存余弦），SQL 由 GORM 生成，建表 DDL 由 [migrations/](migrations/postgres/000001_init.up.sql) 迁移文件独占。

## 包结构

根包 store 是**门面**：负责连接（`Open`/`OpenWith`）、连接池调优、golang-migrate 集成，并把领域子包的 Store 聚合到一个对象上（`s.Servers` / `s.Credentials` / `s.APIKeys` / `s.Logs` / `s.Categories` / `s.Embeddings`）。

| 子包 | 内容 |
| --- | --- |
| `servers/` | `Server`/`Config`/`Tool`、CRUD、`SaveProbe`、工具缓存整体替换、一次性引导名单 `boot_enabled_tools` |
| `credentials/` | `Credential`、密钥池 CRUD、人工启停与自动弃用 |
| `apikeys/` | `APIKey`、接入 key 生成（明文只出现一次）/校验/启停 |
| `logs/` | `LogEntry`/`LogQuery`、写入/筛选/分页/统计（LIKE 转义与排序白名单） |
| `categories/` | `Category`、英文名校验（`checkCategorySlug`）、CRUD 与计数 |
| `embeddings/` | `Embedding`/`Generation`、向量检索与多代管理（保存/清理/GC） |
| `storeutil/` / `storetest/` / `tests/` | `storeutil`：`ErrNotFound`、`NewID`、`RequireAffected`、唯一冲突翻译、JSON 编解码；`storetest`：每个测试建随机命名的独立 PostgreSQL 库、结束强制删除；`tests`：黑盒集成测试（`package tests`，独立目录以避免 import 根包造成循环） |

依赖方向：`子包 → storeutil`，根包 → 所有子包；子包之间互不引用。

## 设计思路

1. **DDL 与读写分离。** 建表全在 migrations/*.sql，GORM 只做增删改查，永不跑 AutoMigrate（它会读错列清单并触发丢数据的"修复"）。迁移由 golang-migrate 内嵌执行，`schema_migrations` 记录版本并带 advisory lock；改 schema 只新增 `00000N_xxx.up.sql` / `.down.sql`。
2. **Row 与领域结构体分离，存储类型从朴素。** `serverRow` 等只声明列映射，对外的 `Server`、`Credential` 由 `toServer()` 等转换。列只用 TEXT / INTEGER / BIGINT（JSON 存 TEXT，布尔 0/1，时间戳 epoch 毫秒）；例外是 `tool_embeddings.vector`：PostgreSQL 用 pgvector `vector`，SQLite 为 TEXT。

**错误约定**：哨兵错误分散在领域子包，供上层映射 HTTP 状态码：

| 错误（所在子包） | 含义 | 上层应映射 |
| --- | --- | --- |
| `storeutil.ErrNotFound` | 记录不存在 | 404 |
| `servers.ErrKeyTaken` / `categories.ErrCategoryTaken` / `categories.ErrCategorySlugTaken` / `credentials.ErrCredentialExists` | 唯一名冲突 | 409 |
| `categories.ErrCategoryInUse` | 分类下还有 server，不能删 | 409 |
| `categories.ErrCategorySlugInvalid` / `categories.ErrCategoryNameRequired` / `apikeys.ErrAPIKeyNameRequired` | 入参不合规 | 400 |

唯一名冲突采用"先查一次给可读错误 + 唯一索引兜底并发竞态"；`storeutil.IsUniqueViolation` 把漏网的冲突翻译成上述哨兵错误，而不是抛 500。

## 业务模型

- **servers / tools** — servers 存下游配置（transport、URL/命令、headers、超时、协议版本）与探活快照；tools 是探活成功后**整体替换**的工具缓存，`exposed_name` 为 `serverKey__toolName`，路由仍走 server_id + name，启停只看 `enabled`。
- **credentials** — 一行 = 一组完整注入头（Bearer / X-API-Key / 多头），整组才可独立轮换。`Enabled` 是人工开关，`State`（`CredState`：active / disabled）是自动判决，两者正交；冷却只在内存，运行态 `downstream.KeyState` 多一个 cooling。
- **api_keys** — `/mcp` 接入 key，只存 sha256，明文仅在创建响应出现一次。按"存在"而非"启用"判断鉴权：一把都没建过时开放，建过就强制。
- **call_logs** — 每次调用现场：入参与返回体截断到 8KB，归因到下游密钥与接入 key；`api_key_name` 存当时快照。排序列走白名单，防 ORDER BY 注入。
- **categories** — server 的单值分类（标签由 servers.tags 承担）；`Name` 与 `Slug`（`[a-z0-9-]`）必填且全局唯一，不做转换；分类下还有 server 时拒绝删除。
- **tool_embeddings** — 主键是代身份 `(tool_id, model, dims, spec)`，新代整代建好再原子切换，**没有回滚接口**。PostgreSQL 的 vector 列不带维度（不同代维度可不同），写入 `"[1,2,3]"` 文本，读取 `CAST(vector AS TEXT)`；SQLite 存 TEXT。`idx_tool_embeddings_gen` 收窄代查询，刻意不建 ANN 索引（HNSW/IVFFlat 锁死单一维度，与多代混合维度冲突）。

## 测试

集成测试在 `tests/`，跑在真实 PostgreSQL 上（`storetest` 创建临时库）；纯函数白盒测试留在各子包内。运行：`go test ./internal/store/...`。
