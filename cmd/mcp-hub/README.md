# cmd/mcp-hub — 程序入口

本文件夹是 mcp-hub 的 `main` 包，`go build ./cmd/mcp-hub` 产出的可执行文件就是从这里编译出来的。
它负责三件事：**读取配置**、**提供 CLI 子命令**、**启动网关服务**。具体的业务逻辑（数据库、聚合、检索、HTTP）都在 `internal/` 各包里，这里只是"组装和接线"。

## 文件一览

| 文件         | 职责                                                                                                                        |
| ------------ | --------------------------------------------------------------------------------------------------------------------------- |
| `main.go`    | 入口：加载配置文件 → 执行命令 → 统一处理退出码（`exitError`）                                                              |
| `root.go`    | 根命令组装与子命令注册；`env`、`version` 两个小命令也在这里                                                                 |
| `serve.go`   | `serve` 子命令：启动网关的完整流程（见下文"启动流程"）                                                                      |
| `add.go`     | `add` 子命令：添加/更新一个下游 server 并落库                                                                               |
| `probe.go`   | `probe` 子命令：探活，加 `--db` 可把结果落库                                                                                |
| `spec.go`    | add / probe 共用的输入组装：`buildSpec`、key 推断、`--header` 重复 flag 解析                                                |
| `output.go`  | add / probe 共用的结果展示：探活结果打印、耗时格式化                                                                        |
| `servers.go` | `servers` 子命令：列出已配置的 server                                                                                       |
| `config.go`  | 配置层：`configItems` 声明表 + viper 实例（读 `.env` / 环境变量）、`LoadSettings`（一次读齐、一次报完错）、`mcp-hub env` 清单 |
| `tests/`     | 测试目录（黑盒测试）。Go 的 main 包无法被导入，被测纯函数已抽到 `internal/httpd`（`IsUnbound` / `NormalizeBase`）等可导入包 |

## 整体流程

### 程序启动（所有子命令共用）

```
mcp-hub <子命令>
  │
  ├─ 1. loadConfigFile()    找到第一个命中的 .env 并载入 viper（同时注入进程环境，
  │     供派生的 stdio 子进程继承）
  │     查找顺序：$MCPHUB_ENV_FILE → ./.env → 可执行文件旁的 .env
  │     环境变量里已有的键不会被 .env 覆盖（进程环境优先）
  │     .env 里不认识的 MCPHUB_* 名字点名提醒，不静默忽略
  │     文件解析失败 → 立即报错退出（退出码 2）
  │
  └─ 2. cobra 执行子命令，错误统一由 main 收尾：
        退出码 2 = 用法/配置错误
        退出码 3 = 环境错误（连不上数据库等）
        退出码 1 = 探活失败
        提示信息一律走 stderr，保证 stdout 输出（如 probe --json）是纯数据
```

### `serve` 的启动流程（核心）

```
mcp-hub serve（不接受任何命令行参数，配置全部来自 .env / 环境变量）
  │
  ├─ 1. LoadSettings()        按 configItems 声明表读齐所有配置项：
  │                           必填缺失、类型不对、枚举非法、语料口径写错
  │                           全部攒在同一个错误里一次报完 → 退出码 2
  ├─ 2. newLogger()           按 MCPHUB_LOG_LEVEL 建 zap 日志器
  ├─ 3. 打印配置警告          "能跑但明显不对"的配置只提醒、不拦截
  ├─ 4. store.Open(DB)        连 PostgreSQL，表结构首次连接时自动同步（需先建好空库）
  ├─ 5. openCache()           redis://…（必填）
  ├─ 6. hub.NewServiceWithOptions()   聚合服务本体
  ├─ 7. EmbedManager          配了 EMBED_API_KEY → 后台补齐向量（不阻塞启动）；
  │                           没配 → 检索自动降级 keyword/bm25，网关照常运行
  ├─ 8. 从数据库加载 server   打印 servers / enabled 数量
  ├─ 9. 启动探活（可选）      PROBE_ON_START=true 时先探活一轮再对外服务
  ├─ 10. httpd 启动           监听 /mcp、/healthz、/readyz、/api/overview（SSE 可选）
  ├─ 11. 打印 banner          包含可直接粘贴的客户端接入配置 JSON
  ├─ 12. 周期探活（可选）     PROBE_INTERVAL > 0 时每 45s（默认）探活一轮
  │
  └─ 13. 等待退出：HTTP 出错 → 退出码 3；收到 Ctrl+C / SIGTERM → 10 秒优雅关闭
```

## 子命令与参数

### `serve` — 启动网关

**没有任何命令行参数**（传了会报错）。配置全部来自环境变量，清单用 `mcp-hub env` 查看。

### `add` — 添加/更新下游 server

```
mcp-hub add --key github --url http://host/mcp --header "Authorization: Bearer xxx"
mcp-hub add --key everything --command npx --arg -y --arg @modelcontextprotocol/server-everything
```

| 参数                 | 必填？                        | 作用                                                            |
| -------------------- | ----------------------------- | --------------------------------------------------------------- |
| `--url`              | 与 `--command` **二选一必填** | 远程 MCP 端点 URL（HTTP 类）                                    |
| `--command`          | 与 `--url` **二选一必填**     | stdio 型 server 的可执行文件                                    |
| `--category`         | **新增必填**，更新时可省略    | 分类 id（省略则保留原分类）                                     |
| `--db`               | 选填                          | PostgreSQL 连接串；缺省读环境变量 `MCPHUB_DB`（两者必须有一个） |
| `--key`              | 选填                          | 工具前缀，留空从 url/command 推断（如域名、命令名）             |
| `--name`             | 选填                          | 显示名，留空同 `--key`                                          |
| `--transport`        | 选填                          | `streamable_http` \| `sse` \| `stdio`，留空按参数推断           |
| `--timeout`          | 选填                          | 请求超时（如 `30s`）                                            |
| `--header`           | 选填，可重复                  | HTTP 头，格式 `"Key: Value"`                                    |
| `--arg`              | 选填，可重复                  | stdio 参数（`--arg -y --arg 包名`）                             |
| `--env`              | 选填，可重复                  | stdio 环境变量，格式 `"KEY=VALUE"`                              |
| `--legacy-only`      | 选填                          | 强制旧版协议握手                                                |
| `--protocol-version` | 选填                          | 固定协议版本，留空自动协商                                      |
| `--disabled`         | 选填                          | 只写配置，不启用                                                |
| `--no-probe`         | 选填                          | 只写配置，不探活                                                |

行为：写入数据库后**默认顺手探活一次**并把工具清单落库；探活失败时配置仍保存，但退出码为 1。

### `probe` — 探活（不落库）

```
mcp-hub probe --url http://host/mcp --json
```

| 参数        | 必填？                        | 作用                                                                                             |
| ----------- | ----------------------------- | ------------------------------------------------------------------------------------------------ |
| `--url`     | 与 `--command` **二选一必填** | 远程 MCP 端点 URL                                                                                |
| `--command` | 与 `--url` **二选一必填**     | stdio 可执行文件                                                                                 |
| `--db`      | 选填                          | 指定则把探活结果落库；不加则只测不存                                                             |
| `--json`    | 选填                          | stdout 输出原始 JSON（失败时退出码 1）                                                           |
| 其余        | 选填                          | 同 `add`：`--transport`、`--timeout`、`--header`、`--arg`、`--legacy-only`、`--protocol-version` |

### `servers` — 列出已配置的 server

| 参数   | 必填？ | 作用                                 |
| ------ | ------ | ------------------------------------ |
| `--db` | 选填   | 缺省读 `MCPHUB_DB`（两者必须有一个） |

输出：KEY / TRANSPORT / STATUS / ENABLED / TOOLS / PROTOCOL / TARGET 一览表。

### `env` — 打印全部配置项

无参数。列出每个环境变量的变量名 / 默认值 / 说明，配置前先跑一遍它。

### `version` — 打印版本

无参数。

## 配置项（serve 用）

写在 `.env` 或进程环境变量里。**优先级：进程环境变量 > `.env` > 内置默认值**，所以 `MCPHUB_ADDR=:8080 mcp-hub serve` 这类一次性覆盖不会被陈旧的 `.env` 吃掉。`.env` 只加载第一个命中的文件，查找顺序 `$MCPHUB_ENV_FILE` → `./.env` → 可执行文件旁的 `.env`。

下列全部配置项由 `config.go` 里的 `configItems` 声明表定义，`mcp-hub env` 打印的就是它。这份清单同时驱动三件事：绑定 viper、启动校验、`env` 命令输出，所以不存在"文档与实现两套"的问题。

**校验口径**（都发生在启动阶段，一次报完、退出码 2）：
- 必填项缺失、类型不对（整数 / 布尔 / 时长）、枚举越界、语料字段名拼错、`MCPHUB_CACHE` 不是 `redis://`/`rediss://` 形态 → 直接报错，不猜、不静默回落；
- 值为空（`MCPHUB_ADDR=`）等于没写，用内置默认值；只有 `MCPHUB_RERANK_MODEL=` 例外——写空是"关掉精排"；
- `.env` 里出现不认识的 `MCPHUB_*` 名字 → 启动时点名提醒并忽略（写错名字不会只剩"改了却没生效"）。

### 必填（2 个）

| 变量           | 说明                                                                                                                            |
| -------------- | ------------------------------------------------------------------------------------------------------------------------------- |
| `MCPHUB_DB`    | PostgreSQL 连接串，如 `postgres://user:pw@127.0.0.1:5432/mcphub?sslmode=disable`                                                 |
| `MCPHUB_CACHE` | Redis 连接串，如 `redis://127.0.0.1:6379/0`；支持 `rediss://`（TLS）。连不上直接启动失败，不静默降级                              |

### 选填

**基础运行**

| 变量               | 默认     | 可填值 / 说明                                              |
| ------------------ | -------- | ---------------------------------------------------------- |
| `MCPHUB_ADDR`      | `:9090`  | 监听地址；端口写 `0` 由系统分配                            |
| `MCPHUB_API_KEY`   | 空       | 管理接口鉴权密钥；留空不校验（此时监听非本机地址会打警告） |
| `MCPHUB_LOG_LEVEL` | `info`   | `debug` \| `info` \| `warn` \| `error`                     |
| `MCPHUB_LIST_MODE` | `meta`   | `meta`（tools/list 只列 5 个检索工具）\| `all`（全量）     |
| `MCPHUB_SSE`       | `true`   | `true` \| `false`：是否暴露旧版 SSE 端点                   |
| `MCPHUB_GIN_MODE`  | `release`| `debug` \| `release` \| `test`：HTTP 框架运行模式；`GIN_MODE` 是同义变量（本项优先） |
| `MCPHUB_ENV_FILE`  | 空       | 显式指定 `.env` 路径；只能来自进程环境变量                 |

**探活**

| 变量                      | 默认   | 可填值 / 说明                                  |
| ------------------------- | ------ | ---------------------------------------------- |
| `MCPHUB_PROBE_INTERVAL`   | `45s`  | Go 时长（`45s` / `2m`），`0` 关闭              |
| `MCPHUB_PROBE_ON_START`   | `true` | `true` \| `false`：启动时先探活一轮再对外服务  |
| `MCPHUB_PROBE_BLOCK_AFTER` | `3`   | 整数：同一上游连续探活失败达到该次数后，网关暂停它的工具调用（AI 调用收到明确拦截说明；探活照常、指数退避不变，任一次探活成功即自动解除）。`0` 不拦截 |

**向量检索（不配密钥则自动降级 keyword/bm25，网关照常运行）**

| 变量                      | 默认          | 可填值 / 说明                                                                        |
| ------------------------- | ------------- | ------------------------------------------------------------------------------------ |
| `MCPHUB_EMBED_BASE_URL`   | SiliconFlow   | OpenAI 兼容的 embed 服务地址，只写到 `/v1`；带 `/embeddings` 后缀会自动纠正并提醒    |
| `MCPHUB_EMBED_API_KEY`    | 空            | embed 服务密钥；留空即降级                                                           |
| `MCPHUB_EMBED_MODEL`      | `BAAI/bge-m3` | embed 模型名                                                                         |
| `MCPHUB_EMBED_DIMS`       | `1024`        | 整数，向量维度，必须与模型一致；填错会在写库前报错                                   |
| `MCPHUB_EMBED_BATCH_SIZE` | `64`          | 整数，每批送多少条文本给 embed 接口                                                  |
| `MCPHUB_CORPUS_FIELDS`    | `all`         | `toolName,toolDescription,tags,serverDescription,schemaHints` 的子集，或 `all`       |

**精排（rerank，不配则用 RRF 融合排序）**

| 变量                     | 默认                      | 可填值 / 说明                                |
| ------------------------ | ------------------------- | -------------------------------------------- |
| `MCPHUB_RERANK_MODEL`    | `BAAI/bge-reranker-v2-m3` | 显式写空（`MCPHUB_RERANK_MODEL=`）即关闭精排 |
| `MCPHUB_RERANK_BASE_URL` | 空（复用 embed）          | 与 embed 不同源时才需要                      |
| `MCPHUB_RERANK_API_KEY`  | 空（复用 embed）          | 与 embed 不同源时才需要                      |

改了 `EMBED_MODEL` / `EMBED_DIMS` 后重启会重建向量，期间检索退回 keyword/bm25，构建完成自动恢复。

### 业务侧怎么读配置

配置只有一个出处（上面的声明表 + viper 实例），业务代码不再自己读环境变量，按场景三选一：

| 场景 | 用法 | 例 |
| --- | --- | --- |
| serve 启动要用整套配置 | `LoadSettings()`：一次读齐、类型解析、校验一次报完 | `serve.go` |
| 子命令只要一两项 | `StringConfig(env)`：只取值、不校验、不填默认值 | `add` / `servers` 的 `--db` 缺省值 |
| `internal/` 业务包 | 不碰配置层，靠调用方把值装进 Options 传进来 | `httpd.Options.GinMode`、`retrieval.EmbedOptions` |

最后一条是刻意的：`internal/` 包不依赖 viper，配置怎么来的它们不关心，测试也就不用摆弄环境变量。业务侧要做的"校验"（如连接串形态）也尽量声明进 `configItems`（`kindRedisURL`），让启动阶段一次报完，而不是散在业务代码里。

## 最小上手路径

```bash
# 1) 起好 PostgreSQL，准备一个空库，写 .env
echo 'MCPHUB_DB=postgres://user:pw@127.0.0.1:5432/mcphub?sslmode=disable' > .env

# 2) 加一个下游 server（add 会探活并把工具清单写库）
mcp-hub add --key deepwiki --url https://mcp.deepwiki.com/mcp --category <分类id>

# 3) 启动网关
mcp-hub serve
# 看到 banner 后，把下面这段贴进 MCP 客户端：
# { "mcpServers": { "mcp-hub": { "url": "http://127.0.0.1:9090/mcp" } } }
```
