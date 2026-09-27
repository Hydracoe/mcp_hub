# store — mcp-hub 持久层

mcp-hub 唯一的持久化包。所有数据落在 PostgreSQL（pgvector 扩展，`tool_embeddings` 的向量列依赖它；SQLite 无向量类型，检索走内存余弦），SQL 由 GORM 生成，建表 DDL 由 [migrations/](migrations/000001_init.up.sql) 迁移文件独占。

## 包结构

根包 store 是**门面**：负责连接（`Open`/`OpenWith`）、连接池调优、golang-migrate 集成，并把六个领域子包的 Store 聚合到一个对象上：

```go
s, err := store.Open(dsn)
s.Servers.Upsert(ctx, servers.Config{...})
s.Credentials.Insert(ctx, cred)
s.APIKeys.Create(ctx, name)
s.Logs.Append(ctx, entry)
s.Categories.Create(ctx, name, slug, desc)
s.Embeddings.Save(ctx, rows)
```

| 子包         | 内容                                                                                               |
| ------------ | -------------------------------------------------------------------------------------------------- |
| `servers/`   | `Server`/`Config`/`Tool`、CRUD、探活结果落库（`SaveProbe`）、工具缓存整体替换、一次性引导名单（`boot_enabled_tools`）         |
| `credentials/` | `Credential`、密钥池增删改查、人工启停与自动弃用                                                 |
| `apikeys/`   | `APIKey`、接入 key 的生成（明文只出现一次）/校验/启停                                              |
| `logs/`      | `LogEntry`/`LogQuery`、日志写入/筛选/分页/统计（含 LIKE 转义与排序白名单）                         |
| `categories/` | `Category`、英文名校验（`checkCategorySlug`，不合规即报错、不做改写）、分类 CRUD 与计数                    |
| `embeddings/` | `Embedding`/`Generation`、向量检索与多代管理（保存/清理/GC）                                     |
| `storeutil/` | 叶子包：`ErrNotFound`、`NewID`、`RequireAffected`、唯一冲突翻译、JSON 列编解码等跨子包共享的小工具 |
| `storetest/` | 测试辅助：每个测试创建一个随机命名的独立 PostgreSQL 库，结束后强制删除，可安全并行                 |
| `tests/`     | 黑盒集成测试（`package tests`）。放独立目录是因为需要 import 根包 store.Open，包内测试会造成循环   |

依赖方向只有一个：`子包 → storeutil`，根包 → 所有子包。子包之间互相不引用；个别跨表 SQL（如分类计数）按表名写（`Table("servers")`），不 import 别的子包。

## 设计思路

四条核心决定，读代码前先记住它们：

1. **DDL 与读写分离。** 建表全部写在 migrations/*.sql 迁移文件里，GORM 只负责增删改查，永远不跑 AutoMigrate。AutoMigrate 靠解析已有表的 DDL 来猜差异，遇到行内注释会读错列清单，进而触发 DROP TABLE 式"修复"——一条会丢数据的路径。

2. **版本化迁移（golang-migrate）。** 迁移文件内嵌在二进制里，`Open` 时自动执行 `migrate Up`；已执行到哪个版本由 golang-migrate 自建的 schema_migrations 表记录，并自带 advisory lock，多实例并发启动不会重复执行。改 schema 时新增一个 `00000N_xxx.up.sql` / `.down.sql`，不改历史文件。

3. **Row 结构体与领域结构体分离。** 各子包里的 `serverRow` 等是"存进库里的样子"（只声明列映射），对外的 `Server`、`Credential` 等是领域结构体，由 `toServer()` 之类的转换函数衔接。分离后改一个列名不会波及上层，反过来也一样。

4. **存储类型从朴素。** 只用 TEXT / INTEGER / BIGINT：JSON（headers、tags、args…）存 TEXT 字符串，布尔存 0/1，时间戳一律存 epoch 毫秒的 BIGINT（integer 32 位撑不过一个月）。不碰 timestamp 与时区语义。唯一的例外是 `tool_embeddings.vector`：PostgreSQL 上用 pgvector 的 vector 类型换原生距离算子（000007），SQLite 上仍是 TEXT。

**错误约定**：哨兵错误分散在各自领域子包，供上层映射 HTTP 状态码——

| 错误（所在子包）                                                                                        | 含义       | 上层应映射 |
| ------------------------------------------------------------------------------------------------------- | ---------- | ---------- |
| `storeutil.ErrNotFound`                                                                                 | 记录不存在 | 404        |
| `servers.ErrKeyTaken` / `categories.ErrCategoryTaken` / `categories.ErrCategorySlugTaken` / `credentials.ErrCredentialExists` | 唯一名冲突 | 409        |
| `categories.ErrCategoryInUse`                                                                           | 分类下还有 server，不能删 | 409        |
| `categories.ErrCategorySlugInvalid` / `categories.ErrCategoryNameRequired` / `apikeys.ErrAPIKeyNameRequired` | 入参不合规 | 400        |

唯一名冲突采用"先查一次给可读错误 + 唯一索引兜底并发竞态"的双保险；`storeutil.IsUniqueViolation` 把漏网的唯一冲突也翻译成上面的哨兵错误，而不是让 500 抛给用户。

## 业务模型

hub 的角色是 **MCP 网关**：上游接外部 AI 客户端，下游代理一批 MCP server，把它们的工具聚合成一个统一的工具池。围绕这个角色有八张表：

- **servers**（servers/）— 下游 MCP server 的配置（transport、URL/命令、headers、超时、协议版本）加探活快照（status、延迟、最近错误）。工具的启停只由 tools 表的 `enabled` 决定。
- **tools**（servers/）— 探活成功后**整体替换**的工具缓存（增量更新会让已删除的工具永远残留，AI 会反复调用它们）。`exposed_name` 是 `serverKey__toolName` 的物化结果，路由仍走 server_id + name 显式映射。
- **credentials**（credentials/）— 下游密钥池。**一行 = 一组完整的注入头**（Bearer / X-API-Key / 多头组合），只有整组才是一次可独立轮换的单位。`Enabled` 是人工开关，`State`（枚举 `CredState`：active / disabled）是自动判决，两者正交。冷却（限流）状态刻意只存在内存——重启往往就是为了恢复，落库只会让新进程背旧账，因此 `State` 是持久枚举，运行态枚举 `downstream.KeyState`（多一个 cooling）由 `CredState.KeyState()` 映射而来。
- **api_keys**（apikeys/）— 外部 AI 客户端调 hub 的 `/mcp` 端点用的接入 key。只存 sha256，明文仅在创建响应里出现一次。一把 key 都没建过时端点开放，建过就强制鉴权（按"存在"而非"启用"判断——全停用等于锁死，不是放开匿名）。
- **call_logs**（logs/）— 每次工具调用的现场：入参与返回体截断到 8KB 落库，归因到具体下游密钥与接入 key。`api_key_name` 存调用当时的名字快照——改名或删 key 不该改写历史。日志查询的排序列走白名单映射，防止 ORDER BY 注入。
- **categories**（categories/）— server 的单值分类（一个 server 至多属一个分类，多标签由 servers.tags 承担）。`Name`（中文）与 `Slug`（英文，`[a-z0-9-]`）都是必填且各自全局唯一，两者之间不做任何转换：填错就报错，不替用户折成别的值。分类下还有 server 时拒绝删除——删分类顺手把 server 摘成"未分类"是一次用户没点过的批量改动。
- **tool_embeddings**（embeddings/）— 工具向量，支撑混合检索（hybrid）里的向量一路。主键是完整"代身份" `(tool_id, model, dims, spec)`：换模型或换语料字段即新代，新代整代建好再原子切换（热切换），切换期间新旧两代并存；**没有回滚接口**——当前代就绪后其它代立即清除。vector 列在 PostgreSQL 上存 pgvector 的 `vector` 类型（000007 起，不带维度：不同代维度可以不同，定长列会拒收第二个维度；写入传 `"[1,2,3]"` 文本由输入函数解析，读取显式 `CAST(vector AS TEXT)` 固定形态），SQLite 上存 TEXT（JSON 数组）、检索走 Go 侧内存余弦。`idx_tool_embeddings_gen (model, dims, spec)` 收窄代查询；刻意不建 ANN 索引——HNSW/IVFFlat 建索引时锁死单一维度，与多代混合维度并存冲突，工具量级下精确全扫是毫秒级。

## 测试

集成测试统一放 `tests/` 黑盒目录，跑在真实 PostgreSQL 上（由 storetest 创建临时库）。纯函数（如 logs 的 `likePattern`）的白盒测试留在各子包内，不依赖根包。运行：

```sh
go test ./internal/store/...
```
