# cmd/mcp-hub — 程序入口

本文件夹是 mcp-hub 的 `main` 包，`go build ./cmd/mcp-hub` 产出的可执行文件就是从这里编译出来的。
它负责三件事：**读取配置**、**提供 CLI 子命令**、**启动网关服务**。具体的业务逻辑（数据库、聚合、检索、HTTP）都在 `internal/` 各包里，这里只是"组装和接线"。

而"把哪些东西组装成什么"这件事本身，已经抽到了 `internal/engine`——本包 `serve.go` 现在只剩
"读配置 → 起引擎 → 打横幅 → 等信号"。这样做的直接好处是**桌面端（`cmd/mcp-hub-desktop`）不必重写一遍装配**：
两个入口共用同一个引擎，业务行为、探活调度、优雅退出的语义因此不会有第二份实现。

构建入口在**项目根**，不在本目录（与 `mcp-hub/` 同级的 `Makefile`）。它同时编排后端、前端
（`mcp-hub-admin`）和 `docker-compose.yml`，后两者都在本仓库之外；产物反过来落在本
仓库的 `bin/` 里——编排脚本跟着编排对象走，构建产物跟着代码走。所以命令都在项目根敲：

```bash
cd ../../..                  # 到项目根，那里才有 Makefile
make build                  # → mcp-hub/bin/mcp-hub
make run                    # 构建并 serve（会自动 cd 回本目录跑，见下）
```

`make run` / `make servers` / `make probe` 会在**本目录**里执行二进制。这不是路径洁癖：
命令行形态的 dataDir 取进程工作目录，而配置文件查找是
`$MCPHUB_ENV_FILE → ./.env → 可执行文件旁的 .env`。从项目根直接跑
`./mcp-hub/bin/mcp-hub` 会找不到 `.env`，静默退回"零配置"（SQLite + 内存缓存），
而 `.env` 里写着 PostgreSQL。

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
| `store.go`   | `openStore`：add / servers / probe 共用的"按配置打开库"入口                                                                 |
| `tests/`     | 测试目录（黑盒测试）。Go 的 main 包无法被导入，被测纯函数已抽到 `internal/httpd`（`IsUnbound` / `NormalizeBase`）等可导入包 |

配置层（`configItems` 声明表、viper 实例、`Load`、`mcp-hub env` 的清单）已经搬到 `internal/config`：
**两条路线（命令行与桌面端）共用同一份配置声明**，否则新增一个配置项要改两处、且必然有一处会忘。
桌面入口见 `../mcp-hub-desktop/`。

两者还有一处刻意的差异：**配置文件的查找口径不同**。命令行是
`$MCPHUB_ENV_FILE → ./.env → 可执行文件旁的 .env`；桌面端是
`$MCPHUB_ENV_FILE → <应用数据目录>/.env`，**不回退到当前工作目录**。
桌面应用的工作目录完全不可控（双击启动时是 `/` 或 `System32`），
让"读到哪份配置"取决于你在哪敲的命令是那种只在别人机器上复现的 bug。
实现在 `config.LoadFile` / `config.LoadFileFrom`，两种口径共用
`resolveConfigFile`。

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
  ├─ 1. config.Load()          按 configItems 声明表读齐所有配置项：
  │                            类型不对、枚举非法、语料口径写错、
  │                            后端与连接串不匹配 → 全部攒在同一个错误里
  │                            一次报完 → 退出码 2
  │
  ├─ 2. engine.New(...)        ↓ 以下全部在 internal/engine（装配层）里，
  │                              桌面入口 cmd/mcp-hub-desktop 走同一条路
  │     ├─ 2.1 store.OpenWith()  按 ResolvedDBDriver() 选方言：
  │     │                          postgres → PostgreSQL（探测 pgvector）
  │     │                          sqlite   → 纯 Go SQLite，表结构首次连接
  │     │                                     时自动同步；连接串留空则在
  │     │                                     DataDir 下建 mcp-hub.db
  │     ├─ 2.2 cache.Open()      按 ResolvedCacheDriver()：
  │     │                          redis → 连不上直接失败，不静默降级
  │     │                          memory → 进程内缓存（默认）
  │     ├─ 2.3 hub.NewServiceWithOptions()   聚合服务本体
  │     ├─ 2.4 EmbedManager     配了 EMBED_API_KEY → 后台补齐向量（不阻塞启动）；
  │     │                       没配 → 检索自动降级 bm25，网关照常运行
  │     └─ 2.5 从数据库加载 server，打印 servers / enabled 数量
  │
  ├─ 3. engine.Start()         先监听、再探活：
  │     ├─ 3.1 httpd 启动        监听 /mcp、/healthz、/readyz、/api/*，
  │     │                        并在路由表最后挂上内嵌界面（有产物才挂）
  │     ├─ 3.2 启动探活（可选）  PROBE_ON_START=true 时探活一轮
  │     └─ 3.3 周期探活（可选）  PROBE_INTERVAL > 0 时每 45s（默认）探活一轮
  │
  ├─ 4. printBanner()          含"管理界面"与可直接粘贴的客户端接入配置 JSON
  │
  └─ 5. 等待退出：HTTP 出错 → 退出码 3；收到 Ctrl+C / SIGTERM →
        engine.Stop() 顺序收尾（停 HTTP → 取消后台协程 → 关业务服务与连接），
        stdio 子进程在这一步退出，不会留下孤儿进程
```

顺序上"先监听、再探活"是刻意的：业务服务在构造时已经从库里装好了上一次的工具快照，
端口一开就能给出（可能略旧的）工具列表，而"还在探活"由 `/readyz` 表达。反过来先探活
会让 `/readyz` 在最需要它的那段时间里连不上——桌面端尤其明显：窗口起来了却拿不到任何响应。


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
| `--db`               | 选填                          | 数据库连接串；缺省读环境变量 `MCPHUB_DB`，两者都空则用当前目录下的 `mcp-hub.db`（SQLite） |
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

| 参数   | 必填？ | 作用                                                                    |
| ------ | ------ | ----------------------------------------------------------------------- |
| `--db` | 选填   | 缺省读 `MCPHUB_DB`；两者都空则用当前目录下的 `mcp-hub.db`（SQLite）      |

输出：KEY / TRANSPORT / STATUS / ENABLED / TOOLS / PROTOCOL / TARGET 一览表。

### `env` — 打印全部配置项

无参数。列出每个环境变量的变量名 / 默认值 / 说明，配置前先跑一遍它。

### `version` — 打印版本

无参数。

## 配置项（serve 用）

写在 `.env` 或进程环境变量里。**优先级：进程环境变量 > `.env` > 内置默认值**，所以 `MCPHUB_ADDR=:8080 mcp-hub serve` 这类一次性覆盖不会被陈旧的 `.env` 吃掉。`.env` 只加载第一个命中的文件，查找顺序 `$MCPHUB_ENV_FILE` → `./.env` → 可执行文件旁的 `.env`。

下列全部配置项由 `config.go` 里的 `configItems` 声明表定义，`mcp-hub env` 打印的就是它。这份清单同时驱动三件事：绑定 viper、启动校验、`env` 命令输出，所以不存在"文档与实现两套"的问题。

**校验口径**（都发生在启动阶段，一次报完、退出码 2）：
- 类型不对（整数 / 布尔 / 时长）、枚举越界、语料字段名拼错 → 直接报错，不猜、不静默回落；
- **后端 × 连接串必须自洽**：显式 `MCPHUB_DB_DRIVER=postgres` 却给了个不像 DSN 的串、
  `=sqlite` 却给了个 `postgres://`、`MCPHUB_CACHE_DRIVER=redis` 却给了空串或非 `redis://`
  形态 → 报错并说明该填什么。绝不悄悄换成另一个后端（那会把"配错了"变成"数据不知去哪了"）；
- 值为空（`MCPHUB_ADDR=`）等于没写，用内置默认值；只有 `MCPHUB_RERANK_MODEL=` 例外——写空是"关掉精排"；
- `.env` 里出现不认识的 `MCPHUB_*` 名字 → 启动时点名提醒并忽略（写错名字不会只剩"改了却没生效"）。

### 后端选型（4 个变量，全部可选）

两个后端各由"驱动 + 连接串"两项描述。**驱动留 `auto` 就够用**：它按连接串的形态推断，
只有"库文件放在一个不像路径的位置"这类少见情形才需要显式写死。

| 变量                  | 默认   | 说明                                                                                     |
| --------------------- | ------ | ---------------------------------------------------------------------------------------- |
| `MCPHUB_DB`           | 空     | `postgres://...` 走 PostgreSQL；文件路径（如 `./data/mcp-hub.db`）走 SQLite；留空见下      |
| `MCPHUB_DB_DRIVER`    | `auto` | `auto` \| `postgres` \| `sqlite`                                                          |
| `MCPHUB_CACHE`        | 空     | `redis://...` / `rediss://...` 走 Redis；留空走进程内缓存                                  |
| `MCPHUB_CACHE_DRIVER` | `auto` | `auto` \| `redis` \| `memory`                                                             |

**两个留空就是可用的默认形态**（SQLite + 进程内缓存），这是"先跑起来看看"的路径：
不需要先起数据库，也不会因为 Redis 没开而启动失败。库里放哪由形态决定——
命令行形态是当前工作目录，桌面形态是系统的应用数据目录。

三点需要知道的差异：

- **数据不互通**。从 SQLite 切到 PostgreSQL 不会迁移任何东西，反之亦然。两边各有自己的
  迁移树（`internal/store/migrations/{postgres,sqlite}/`），SQLite 侧是合并后的基线。
- **向量检索**。PostgreSQL 装了 pgvector 走原生路径；SQLite 没有向量类型，退回 Go 内存余弦，
  语义一致（都用 `1 - 余弦相似度` 当距离），代价是候选集要读进内存。工具量到万级再考虑换。
- **内存缓存不跨进程**。多实例部署必须用 Redis，否则每个实例各探各的活、各算各的检索缓存。
  启动时会有一条 warn 提醒，不会假装它是共享的。

### 管理界面：内嵌，还是不带

界面归部署形态管，不归业务管。二进制里带不带界面，由构建时有没有跑 `make ui` 决定：

- **带界面**（`make ui && make build`，或 `make release`）：前端产物被 `go:embed` 打进二进制，
  挂在路由表的最后（gin 的 NoRoute）。`/api`、`/mcp` 的语义完全不受影响——
  保留命名空间下的未命中仍然是 404，不会被 SPA 回退吃掉。
- **不带界面**（直接 `make build`）：`internal/webassets.Available()` 返回 false，
  引擎根本不挂界面，`/` 得到干净的 404。启动横幅会说明这一点并给出补上界面的命令。

两种形态下 `/api` 与 `/mcp` 的行为一字不差。

### 容器部署

容器里跑的是**服务端形态**（`cmd/mcp-hub`），界面在构建期就嵌进去了——
`Dockerfile` 的第一阶段专门用来构建前端。桌面形态（`cmd/mcp-hub-desktop`）
不进镜像：它需要 Cocoa/GTK webview，容器里没有窗口系统可承载。

编排文件在**仓库外**（与 `mcp-hub/` 同级的 `docker-compose.yml`），因为它同时管着后端、
PostgreSQL、Redis 三者。基础镜像清单、容器内的配置边界、`MCPHUB_ADDR` 为什么
必须是 `:9090`（不是 `127.0.0.1:9090`）、stdio 下游在 alpine 里的限制——
都写在 [`docs/deployment.md`](../../docs/deployment.md)。

### 选填

**基础运行**

| 变量               | 默认     | 可填值 / 说明                                              |
| ------------------ | -------- | ---------------------------------------------------------- |
| `MCPHUB_ADDR`      | `127.0.0.1:9090` | 监听地址；端口写 `0` 由系统分配                            |
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

**向量检索（不配密钥则自动降级 bm25，网关照常运行）**

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

改了 `EMBED_MODEL` / `EMBED_DIMS` 后重启会重建向量，期间检索退回 bm25，构建完成自动恢复。

### 业务侧怎么读配置

配置只有一个出处（上面的声明表 + viper 实例），业务代码不再自己读环境变量，按场景三选一：

| 场景 | 用法 | 例 |
| --- | --- | --- |
| serve 启动要用整套配置 | `config.Load()`：一次读齐、类型解析、校验一次报完 | `serve.go`、`mcp-hub-desktop/main.go` |
| 子命令只要一两项 | `config.String(env)`：只取值、不校验、不填默认值 | `add` / `servers` 的 `--db` 缺省值 |
| `internal/` 业务包 | 不碰配置层，靠调用方把值装进 Options 传进来 | `httpd.Options.GinMode`、`retrieval.EmbedOptions` |

最后一条是刻意的：`internal/` 包不依赖 viper，配置怎么来的它们不关心，测试也就不用摆弄环境变量。业务侧要做的"校验"（如连接串形态）也尽量声明进 `configItems`（`kindRedisURL` 等），让启动阶段一次报完，而不是散在业务代码里。

配置包（`internal/config`）不 import 任何业务包，只依赖 viper——所以它可以被任何入口安全地引用，
而不需要反过来认识"谁来跑它"。这是"配置 → 装配 → 业务"这条单向依赖的起点。

## 最小上手路径

```bash
# 1) 零配置直接起（SQLite + 进程内缓存，库落在当前目录的 mcp-hub.db）
#    项目根敲 make run，或者在本目录直接 ./bin/mcp-hub serve
mcp-hub serve
# 或者在 .env 里显式写死：
#   MCPHUB_DB=./data/mcp-hub.db
#   MCPHUB_CACHE=

# 2) 加一个下游 server（add 会探活并把工具清单写库）
mcp-hub add --key deepwiki --url https://mcp.deepwiki.com/mcp --category <分类id>

# 3) 重启网关（配置里的探活间隔会自动带上新 server）
mcp-hub serve
# 看到 banner 后，把下面这段贴进 MCP 客户端：
# { "mcpServers": { "mcp-hub": { "url": "http://127.0.0.1:9090/mcp" } } }
```

需要 PostgreSQL + Redis 那种多实例部署时，在 `.env` 里写好这两个连接串即可，命令与上面完全一样：

```bash
MCPHUB_DB=postgres://mcphub:mcphub@127.0.0.1:5432/mcphub?sslmode=disable
MCPHUB_CACHE=redis://127.0.0.1:6379/0
```

要带上管理界面，构建时多一步（只需一次，产物会被打进二进制）：

```bash
make ui && make build      # 等价于 make release
```

想要一个双击就能用的桌面版本（Wails 原生窗口，需要 cgo）：

```bash
make build-desktop && ./mcp-hub/bin/mcp-hub-desktop
```

没装 GUI 工具链的机器上还有浏览器壳，无 cgo、可自由交叉编译：

```bash
make build-desktop-browser && ./mcp-hub/bin/mcp-hub-desktop-browser
```
