# cmd/mcp-hub — 程序入口

本文件夹是 mcp-hub 的 `main` 包，`go build ./cmd/mcp-hub` 产出的可执行文件从这里编译。它负责读取配置、提供 CLI 子命令、启动网关服务；业务逻辑在 `internal/` 各包里，装配由 `internal/engine` 完成，`cmd/mcp-hub-desktop` 共用同一引擎。

构建入口在**仓库根**，`Makefile` 与 `mcp-hub/` 同级，产物落在 `mcp-hub/bin/`：

```bash
cd ../../..                  # 到仓库根，那里才有 Makefile
make build                  # → mcp-hub/bin/mcp-hub
make run                    # 构建并 serve（会自动 cd 回本目录跑）
```

`make run` / `make servers` / `make probe` 会在**本目录**里执行二进制。命令行形态的 dataDir 取进程工作目录，配置查找是 `$MCPHUB_ENV_FILE → ./.env → 可执行文件旁的 .env`；从仓库根直接跑 `./mcp-hub/bin/mcp-hub` 会找不到 `.env`，静默退回零配置（SQLite + 内存缓存）。

## 文件一览

入口是 `main.go`（加载配置 → 执行 → 统一退出码 `exitError`）、`root.go`（根命令及 `env` / `version`）、`serve.go`（启动流程）。子命令各自成文件：`add.go`、`probe.go`、`servers.go`；`spec.go`（`buildSpec`、key 推断、`--header` 解析）与 `output.go`（结果展示）由 add / probe 共用；`store.go` 提供 `openStore`；`tests/` 是黑盒测试，纯函数已抽到 `internal/httpd`（`IsUnbound` / `NormalizeBase`）等可导入包。

配置层（`configItems` 声明表、viper 实例、`Load`、`mcp-hub env` 清单）在 `internal/config`，命令行与桌面端共用。配置文件查找口径不同：命令行是 `$MCPHUB_ENV_FILE → ./.env → 可执行文件旁的 .env`；桌面端是 `$MCPHUB_ENV_FILE → <应用数据目录>/.env`，**不回退到当前工作目录**。实现在 `config.LoadFile` / `config.LoadFileFrom`，共用 `resolveConfigFile`。

## 整体流程

所有子命令共用启动步骤，以及 `serve` 的流程（`serve` 不接受任何命令行参数，配置全部来自 `.env` / 环境变量）：

```
mcp-hub <子命令>
  ├─ loadConfigFile()    $MCPHUB_ENV_FILE → ./.env → 可执行文件旁 .env；进程环境优先，
  │                      env 注入供 stdio 子进程继承；未知 MCPHUB_* 提醒；解析失败退出码 2
  └─ cobra 执行；退出码 2 = 用法/配置错误，3 = 环境错误，1 = 探活失败；提示走 stderr

mcp-hub serve
  ├─ config.Load()       类型、枚举、语料字段、后端与连接串不匹配一次报完 → 退出码 2
  ├─ engine.New()        store.OpenWith（postgres/pgvector 或 sqlite；sqlite 首次连接自动同步，
  │                      连接串留空则在 DataDir 建 mcp-hub.db）；cache.Open（redis 连不上即失败，
  │                      memory 默认）；hub.NewServiceWithOptions；EmbedManager（无 key 降级 bm25）；
  │                      从库加载 server 并打印数量
  ├─ engine.Start()      先监听 /mcp、/healthz、/readyz、/api/* 与内嵌界面，再按
  │                      PROBE_ON_START / PROBE_INTERVAL（默认 45s）探活
  ├─ printBanner()       含管理界面与可粘贴的客户端接入配置 JSON
  └─ 等退出：HTTP 出错 → 退出码 3；Ctrl+C / SIGTERM → engine.Stop() 顺序收尾
```

先监听再探活：业务服务构造时已装好上一次的工具快照，端口一开就能给出（可能略旧的）工具列表，"还在探活"由 `/readyz` 表达。

## 子命令与参数

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
| `--db`               | 选填                          | 数据库连接串；缺省读 `MCPHUB_DB`，两者都空则用当前目录下的 `mcp-hub.db`（SQLite） |
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

写入数据库后**默认顺手探活一次**并把工具清单落库；探活失败时配置仍保存，但退出码为 1。

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

`--db` 选填，缺省读 `MCPHUB_DB`，两者都空则用当前目录下的 `mcp-hub.db`（SQLite）。输出 KEY / TRANSPORT / STATUS / ENABLED / TOOLS / PROTOCOL / TARGET 一览表。

### `env` / `version`

`env` 无参数，列出每个环境变量的变量名 / 默认值 / 说明；`version` 无参数，打印版本。

## 配置项（serve 用）

写在 `.env` 或进程环境变量里。**优先级：进程环境变量 > `.env` > 内置默认值**。`.env` 只加载第一个命中的文件，查找顺序 `$MCPHUB_ENV_FILE` → `./.env` → 可执行文件旁的 `.env`。

下列配置项由 `config.go` 里的 `configItems` 声明表定义，`mcp-hub env` 打印的就是它；同一份清单驱动绑定、启动校验与 `env` 输出。

**校验口径**（启动阶段一次报完、退出码 2）：类型不对、枚举越界、语料字段名拼错 → 直接报错；**后端 × 连接串必须自洽**（`MCPHUB_DB_DRIVER=postgres` 配非 DSN、`=sqlite` 配 `postgres://`、`MCPHUB_CACHE_DRIVER=redis` 配空串或非 `redis://` 都报错并说明该填什么，绝不悄悄换后端）；值为空（`MCPHUB_ADDR=`）等于没写，用内置默认值，`MCPHUB_RERANK_MODEL=` 例外（写空是关掉精排）；`.env` 里不认识的 `MCPHUB_*` 名字启动时点名提醒并忽略。

### 后端选型（4 个变量，全部可选）

| 变量                  | 默认   | 说明                                                                                |
| --------------------- | ------ | ----------------------------------------------------------------------------------- |
| `MCPHUB_DB`           | 空     | `postgres://...` 走 PostgreSQL；文件路径（如 `./data/mcp-hub.db`）走 SQLite；留空见下 |
| `MCPHUB_DB_DRIVER`    | `auto` | `auto` \| `postgres` \| `sqlite`                                                    |
| `MCPHUB_CACHE`        | 空     | `redis://...` / `rediss://...` 走 Redis；留空走进程内缓存                            |
| `MCPHUB_CACHE_DRIVER` | `auto` | `auto` \| `redis` \| `memory`                                                       |

驱动留 `auto` 即按连接串形态推断。两个连接串都留空就是默认形态（SQLite + 进程内缓存），库位置由形态决定：命令行形态是当前工作目录，桌面形态是系统应用数据目录。SQLite 与 PostgreSQL 各有迁移树（`internal/store/migrations/{postgres,sqlite}/`），切换不迁移数据；PostgreSQL 装 pgvector 走原生向量路径，SQLite 用 Go 内存余弦（距离都是 `1 - 余弦相似度`）；内存缓存不跨进程，多实例部署必须用 Redis。

### 基础运行

| 变量               | 默认            | 可填值 / 说明                                              |
| ------------------ | --------------- | ---------------------------------------------------------- |
| `MCPHUB_ADDR`      | `127.0.0.1:9090` | 监听地址；端口写 `0` 由系统分配                            |
| `MCPHUB_API_KEY`   | 空              | 管理接口鉴权密钥；留空不校验（监听非本机地址会打警告）     |
| `MCPHUB_LOG_LEVEL` | `info`          | `debug` \| `info` \| `warn` \| `error`                      |
| `MCPHUB_LIST_MODE` | `meta`          | `meta`（tools/list 只列 5 个检索工具）\| `all`（全量）     |
| `MCPHUB_SSE`       | `true`          | `true` \| `false`：是否暴露旧版 SSE 端点                   |
| `MCPHUB_GIN_MODE`  | `release`       | `debug` \| `release` \| `test`；`GIN_MODE` 同义（本项优先） |
| `MCPHUB_ENV_FILE`  | 空              | 显式指定 `.env` 路径；只能来自进程环境变量                 |

### 探活

| 变量                      | 默认   | 可填值 / 说明                                  |
| ------------------------- | ------ | ---------------------------------------------- |
| `MCPHUB_PROBE_INTERVAL`   | `45s`  | Go 时长（`45s` / `2m`），`0` 关闭              |
| `MCPHUB_PROBE_ON_START`   | `true` | `true` \| `false`：启动时先探活一轮再对外服务  |
| `MCPHUB_PROBE_BLOCK_AFTER` | `3`   | 同一上游连续探活失败达到该次数后暂停它的工具调用（探活照常、指数退避不变，任一次成功即解除）。`0` 不拦截 |

### 向量检索（不配密钥则自动降级 bm25，网关照常运行）

| 变量                      | 默认          | 可填值 / 说明                                                                     |
| ------------------------- | ------------- | --------------------------------------------------------------------------------- |
| `MCPHUB_EMBED_BASE_URL`   | SiliconFlow   | OpenAI 兼容的 embed 服务地址，只写到 `/v1`；带 `/embeddings` 后缀会自动纠正并提醒 |
| `MCPHUB_EMBED_API_KEY`    | 空            | embed 服务密钥；留空即降级                                                        |
| `MCPHUB_EMBED_MODEL`      | `BAAI/bge-m3` | embed 模型名                                                                      |
| `MCPHUB_EMBED_DIMS`       | `1024`        | 整数，向量维度，必须与模型一致；填错会在写库前报错                                |
| `MCPHUB_EMBED_BATCH_SIZE` | `64`          | 整数，每批送多少条文本给 embed 接口                                               |
| `MCPHUB_CORPUS_FIELDS`    | `all`         | `toolName,toolDescription,tags,serverDescription,schemaHints` 的子集，或 `all`    |

### 精排（rerank，不配则用 RRF 融合排序）

| 变量                     | 默认                      | 可填值 / 说明                                |
| ------------------------ | ------------------------- | -------------------------------------------- |
| `MCPHUB_RERANK_MODEL`    | `BAAI/bge-reranker-v2-m3` | 显式写空（`MCPHUB_RERANK_MODEL=`）即关闭精排 |
| `MCPHUB_RERANK_BASE_URL` | 空（复用 embed）          | 与 embed 不同源时才需要                      |
| `MCPHUB_RERANK_API_KEY`  | 空（复用 embed）          | 与 embed 不同源时才需要                      |

改了 `EMBED_MODEL` / `EMBED_DIMS` 后重启会重建向量，期间检索退回 bm25，构建完成自动恢复。

### 管理界面与部署形态

带界面（`make ui && make build`，或 `make release`）：产物经 `go:embed` 打进二进制，挂在路由表最后（gin 的 NoRoute），`/api`、`/mcp` 的未命中仍是 404。不带界面（直接 `make build`）：`internal/webassets.Available()` 返回 false，`/` 是干净的 404，启动横幅会给出补上界面的命令。容器部署说明（`MCPHUB_ADDR` 为什么必须是 `:9090`、stdio 下游限制等）见 [`docs/deployment.md`](../../docs/deployment.md)。

## 最小上手路径

```bash
# 零配置直接起（SQLite + 进程内缓存，库落在当前目录的 mcp-hub.db）
mcp-hub serve
# 加下游 server（add 会探活并把工具清单写库）
mcp-hub add --key deepwiki --url https://mcp.deepwiki.com/mcp --category <分类id>
# 重启网关后，把这段贴进 MCP 客户端：
# { "mcpServers": { "mcp-hub": { "url": "http://127.0.0.1:9090/mcp" } } }
```

`.env` 也可显式写 `MCPHUB_DB=./data/mcp-hub.db`、`MCPHUB_CACHE=`。多实例部署换成：

```bash
MCPHUB_DB=postgres://mcphub:mcphub@127.0.0.1:5432/mcphub?sslmode=disable
MCPHUB_CACHE=redis://127.0.0.1:6379/0
```

带管理界面：`make ui && make build`（等价于 `make release`）。桌面版：`make build-desktop && ./mcp-hub/bin/mcp-hub-desktop`（需要 cgo）；无 GUI 工具链可用浏览器壳 `make build-desktop-browser && ./mcp-hub/bin/mcp-hub-desktop-browser`（无 cgo、可交叉编译）。
