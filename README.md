# mcp-hub

把多个 MCP server 聚合成单一端点的高性能网关。后端 Go，前端 React（M5）。

设计依据见上层目录的 `PRD.md`。

## 当前进度

| 里程碑 | 内容 | 状态 |
|---|---|---|
| M1 | Go 骨架、SQLite、下游 Client、协议双栈协商 | ✅ |
| M2 | 聚合与路由、`/mcp` 端点、工具缓存、故障隔离 | ✅ |
| M3 | 管理后端 REST API、server 增删改、连通性测试、调用日志 | ✅ |
| M3.1 | 凭据热更新、401 重试、优雅停用、连接指纹 | ✅ |
| M4 | 密钥池与配额治理（多密钥轮换、RPM/TPM、按错误退避或弃用） | ✅ |
| M5 | React 管理界面 + `go:embed` 打包 | 待做 |
| M6 | 真实 AI 客户端接入 + 延迟压测 | 待做 |

## 快速开始

```bash
go build -o bin/mcp-hub ./cmd/mcp-hub

# 1) 添加两个下游（add 会顺带探活一次）
./bin/mcp-hub add --db mcp-hub.db --key deepwiki --url https://mcp.deepwiki.com/mcp
./bin/mcp-hub add --db mcp-hub.db --key fs \
    --command npx --arg -y --arg @modelcontextprotocol/server-filesystem --arg /tmp

# 2) 启动网关
./bin/mcp-hub serve --db mcp-hub.db --addr :9090
```

启动后会打印可直接粘贴的客户端配置：

```json
{ "mcpServers": { "mcp-hub": { "url": "http://127.0.0.1:9090/mcp" } } }
```

## CLI

```
mcp-hub serve [flags]     启动网关：聚合下游工具并对外提供 MCP 端点
mcp-hub add [flags]       添加一个下游 server（会顺带探活一次）
mcp-hub probe [flags]     探活一个下游 MCP server (L1–L3)，不落库
mcp-hub servers --db F    列出已配置的 server
mcp-hub version           打印版本
```

`serve` 常用参数：

| 参数 | 默认 | 说明 |
|---|---|---|
| `--db` | `mcp-hub.db` | SQLite 路径 |
| `--addr` | `:9090` | 监听地址，端口写 `0` 由系统分配 |
| `--probe-interval` | `45s` | 周期探活间隔，`0` 关闭 |
| `--probe-on-start` | `true` | 启动时先探活一轮 |
| `--no-sse` | `false` | 不暴露旧版 SSE 端点 |
| `--api-key` | 空 | 管理接口的访问密钥。留空则不校验，此时会拒绝监听非本机地址 |
| `--log-level` | `info` | `debug` / `info` / `warn` / `error` |

## HTTP 端点

| 路径 | 用途 |
|---|---|
| `POST /mcp` | Streamable HTTP，MCP 主端点（AI 客户端连这个） |
| `GET /sse` + `POST /message` | 旧版 SSE，兼容只认 SSE 的客户端 |
| `GET /healthz` | 存活探针。进程在跑就 200 |
| `GET /readyz` | 就绪探针。有工具可提供即 200，附 `unhealthyNodes` |
| `GET /api/status` | 聚合状态快照 |

管理接口（`/api/*`）见下节。

## 管理接口

新增一个下游不需要重启进程，也不需要停机改配置。

| 方法 | 路径 | 用途 |
|---|---|---|
| GET | `/api/overview` | 概览：server 统计、工具数、近 24h 调用与失败率、密钥状态 |
| GET | `/api/connect` | 接入指引：端点 URL + 可一键粘贴的客户端配置 |
| GET | `/api/servers` | server 列表（含状态、延迟、工具计数） |
| POST | `/api/servers` | 新增。`?probe=1` 顺带探活，`?upsert=1` 允许覆盖同名 |
| GET | `/api/servers/{id}` | 详情（含可编辑配置，密钥打码） |
| PUT | `/api/servers/{id}` | 编辑（整体替换；提交 `***` 表示沿用原密钥） |
| DELETE | `/api/servers/{id}` | 删除（工具缓存级联清理） |
| POST | `/api/servers/{id}/enable` `/disable` | 启停。停用后工具**立即**从聚合列表消失，但连接等正在跑的调用结束才关（见下节） |
| POST | `/api/servers/{id}/test` | 单点连通性测试（L1–L3，返回协议版本与分段耗时） |
| POST | `/api/servers/{id}/refresh-tools` | 只刷工具缓存 |
| POST | `/api/servers/test-all` | 全量测试，返回逐项结果（停用项计入 `disabled`，不计成败） |
| POST | `/api/servers/import` | 批量导入 `mcpServers` 格式配置 |
| GET | `/api/tools` | 聚合工具列表（`?serverKey=` 筛选，`?q=` 搜索） |
| GET | `/api/logs` | 调用日志（`serverId` / `status` / `kind` / `since` / `until` / `limit` / `offset`） |
| DELETE | `/api/logs` | 清空日志 |

密钥池（多密钥轮换，见下节）：

| 方法 | 路径 | 用途 |
|---|---|---|
| GET | `/api/servers/{id}/credentials` | 密钥列表（含运行态：冷却剩余、窗口用量、指纹） |
| POST | `/api/servers/{id}/credentials` | 新增。给 `headers`，或给 `value` + `authScheme` 由服务端展开 |
| POST | `/api/servers/{id}/credentials/import` | 批量粘贴，`lines` 一行一把（`名称=值` 或直接给值） |
| PUT | `/api/servers/{id}/credentials/{cid}` | 编辑。省略 `headers` 或提交 `***` 都表示不动凭据 |
| DELETE | `/api/servers/{id}/credentials/{cid}` | 删除 |
| POST | `/api/servers/{id}/credentials/{cid}/enable` `/disable` | 人工启停 |
| POST | `/api/servers/{id}/credentials/{cid}/test` | 单密钥探活。**只读**，不改密钥状态 |
| POST | `/api/servers/{id}/credentials/{cid}/reset-quota` | 清零本地配额计数（不动冷却） |

```bash
# 新增一个远程 server 并立即验证
curl -X POST 'http://127.0.0.1:9090/api/servers?probe=1' \
  -H 'Content-Type: application/json' \
  -d '{"key":"deepwiki","transport":"streamable_http","url":"https://mcp.deepwiki.com/mcp"}'

# 看看它带来了什么
curl -s http://127.0.0.1:9090/api/tools | head -c 400

# 从客户端配置批量导入
curl -X POST http://127.0.0.1:9090/api/servers/import \
  -H 'Content-Type: application/json' \
  -d '{"mcpServers":{"fs":{"command":"npx","args":["-y","@modelcontextprotocol/server-filesystem","/tmp"]}}}'

# 给一个 server 挂三把密钥并限制速率（一行一把，不用手写 JSON）
curl -X POST http://127.0.0.1:9090/api/servers/$ID/credentials/import \
  -H 'Content-Type: application/json' \
  -d '{"authScheme":"bearer","rpmLimit":600,
       "lines":["主号=sk-aaa","备用1=sk-bbb","备用2=sk-ccc"]}'
```

几处刻意的取舍：

- **密钥只进不出**。`headers` 与 `env` 的值一律以 `***` 回显，键名保留供辨认。
  编辑时原样提交 `***` 表示不改动——否则"改个超时"就会顺手把凭据抹掉。
- **POST 遇到同名 key 返回 409**，不静默覆盖。要覆盖得显式加 `?upsert=1`。
- **测试与刷新是两件事**。`test` 返回诊断（协议版本、分段耗时、失败分类与排查方向），
  `refresh-tools` 返回工具清单。两者都不改变启用状态。
- **全量测试只统计启用的 server**。停用项计入 `disabled` 字段，不进 `healthy` / `failed`，
  因此 `healthy + failed` 恒等于 `total`。停用是主动决定，不该在健康报告里显示成故障。
- **导入不静默改名**。`my_server` 这类非法前缀会被折成 `my-server`，
  并在结果里带 `renamedFrom` 标出原键名。
- **配置变更按"目标是否换了"分流**。换密钥、改过滤名单、改显示名都**不重建连接**；
  只有 url / command / env / 超时 / 协议版本变了才重建。重建会打断在飞调用，
  而用户点这些开关时完全没有这个预期。

停用期间查询 server，响应里会带 `draining` / `drainingInflight` / `drainingSince`——
界面据此显示「停用中 · 2 个调用收尾」，收尾结束自动转成「已停用」。

### 鉴权

默认不校验：服务默认绑 `127.0.0.1`。一旦监听非本机地址（含只写端口的 `:9090`）
且未设 `--api-key`，启动时会打一条警告——管理接口能改配置，等于半个 shell。

开启后，`/api/*` 需要 `X-API-Key: <key>` 或 `Authorization: Bearer <key>`。
`/mcp`、`/healthz`、`/readyz` 不受影响。

## 聚合设计

**命名**：对外工具名统一为 `<serverKey>__<toolName>`，例如 `deepwiki__ask_question`。

**路由**：靠 `Route` 映射表（`exposedName → serverID + toolName`），
**不切分字符串反推**。下游工具名本身可能含 `__`（如 `a__b`），
切分方案会在这种边界上静默串线。

**透传**：

| 环节 | 做法 |
|---|---|
| 入参 | 用 `RawArguments` 原样转发。走解析后的 map 会让超过 2^53 的整数失真 |
| 结果 | `content` / `isError` / `structuredContent` 一字不改。下游标了失败就是失败，网关不替它抹平 |
| 入参 schema | 逐字节发给 AI 客户端。**保真度边界**：mcp-go 客户端会把 schema 解析成 `$defs/type/properties/required/additionalProperties`，顶层 `oneOf`/`anyOf`/`$ref` 不保留（`properties` 内部关键字不受影响） |

**工具缓存的语义**：探活失败**不清空**该 server 的工具缓存，只更新状态。
下游抖动不该让 AI 手上的工具凭空消失。缓存的移除只有两条路径——
重新探活成功（整体替换）或删除 server。

**工具列表稳定性**：聚合列表按指纹比较，内容没变就不调 `SetTools`。
否则每轮周期探活都会广播 `tools/list_changed`，让 AI 不断重新规划。

**故障隔离**：

- 每个下游在独立 goroutine 中带独立超时探活，单点失败只写它自己的状态行
- 下游故障时调用返回 `isError: true` 的**结果**，而不是 JSON-RPC 错误——
  客户端会话不该因为某个下游挂了而判定异常
- 调用路径上有并发上限（每下游 16）与失败退避（2s 起，指数退避至 30s），
  避免一个卡死的下游把网关的 goroutine 占满

## 下游验证设计

验证分四层，前三层自动、第四层手动：

| 层 | 内容 | 动作 | 触发 |
|---|---|---|---|
| L1 可达性 | DNS / TCP / TLS | 原生拨号，不发 MCP 报文 | 自动 |
| L2 协议 | 版本协商、身份、能力 | `server/discover` → 失败回退 `initialize` | 自动 |
| L3 工具清单 | 工具数量与 schema | `tools/list` | 自动 |
| L4 实际调用 | 工具真正执行 | `tools/call` | **仅手动** |

L4 会产生副作用与费用，因此默认不做，且不纳入健康状态判定。

`tools/list` 同时充当健康探针：新版协议已移除 `ping`，
`tools/list` 是唯一既确认存活又能顺带刷新缓存的调用。

### 协议双栈

MCP 在 2026-07-28 做了一次无状态重写（移除 `initialize` 握手与 `Mcp-Session-Id`）。
下游可能是新版也可能是旧版，`mark3labs/mcp-go` 的 `Client.Initialize` 内部已完成
自适应协商，网关只负责读取并**缓存协商结果**，避免每次调用都试错两次。

实测可同时挂载两种时代的 server：

```
deepwiki  streamable_http  healthy  2025-11-25  [legacy]
local     stdio            healthy  2026-07-28  [modern]
```

### 连接生命周期（一个必须避开的坑）

stdio 传输底层是 `exec.CommandContext`——传进 `Start()` 的 ctx 一取消，
**子进程就被杀**。因此常驻连接必须持有独立于请求的 `lifeCtx`（见 `conn.go`），
只有 `Close()` 才取消它。

把某个请求的 ctx 传进去，会出现一种很像"下游不稳定"的症状：
第一个请求成功，其后约一半调用报 `EXIT_EARLY`。实测这个错误会让
stdio 调用的 P50 从 0.29ms 退化到 10.55ms（每次都在重新拉进程）。
`TestStdioDownstreamSurvivesAcrossRequests` 专门守住这条。

### 凭据轮换与优雅停用

两条约定，都源于同一件事：管理操作不该把别人的任务弄挂。

**凭据热更新。** 远程传输的 `headers` 通过 `WithHTTPHeaderFunc` 在**每个请求**
现场注入，而不是建连时写死。改密钥只需换掉回调读到的那份值：

- 连接不重建、不断线
- 已发出的请求仍用它出发时的凭据，正常完成
- 下一个请求自动带上新凭据
- AI 客户端无感知：`tools/list` 不变，不广播 `tools/list_changed`

三种传输的能力不同，这是协议与操作系统决定的，不是实现取舍：

| 传输 | 凭据生效方式 | 轮换效果 |
|---|---|---|
| `streamable_http` | 每个请求现场取 | **完全无感** |
| `sse` | 每次发消息现场取（接收通道仍是建连时那份） | 基本无感 |
| `stdio` | 子进程启动时注入环境变量 | **做不到**，必须重启进程，走优雅重建 |

**401 自动重试。** 热更新只管得住"之后的请求"。如果轮换时旧密钥已被下游吊销，
此刻恰好在飞的那次调用会收到 401——这唯一的缝由一条重试补上：**请求往返期间
凭据版本变过，且失败是 401/403，就用新凭据重试一次。**

两个条件都必需：

- **只对 401/403 重试。** 这类响应意味着请求**没有被执行**，所以重试不会让
  有副作用的工具跑第二遍。超时与 5xx 可能已经执行过，绝不能重试。
- **只在凭据版本变过时重试。** 否则就是凭据本身错了，重试只会多打一次无效请求，
  还会把"密钥填错了"这个明确信号糊成"偶发失败"。

**停用是两阶段的。** 点"停用"之后：

1. 工具**立即**从聚合列表撤下——AI 从这里起不会再派新调用过来
2. 连接进入收尾：拒绝新调用，在飞的照常跑完
3. 在飞归零后关闭连接（远程断开 HTTP，stdio 结束子进程）

等待上限 1 分钟（`Pool.DrainTimeout`）。超时则强制关闭，并把仍在跑的调用标记为
`CANCELED`——没有这个上限，一个卡死的下游会让该 server 永远停在「停用中」，
而用户没有任何办法。注意"强制关闭"必须真的取消在飞请求，只是把连接从池里摘掉
并不够：已经发出的 HTTP 请求有它自己的生命周期。

配置变更共用这套机制：url / command / env 变了，旧连接优雅退场，新连接立刻可用。
在飞调用仍打在旧地址上——它本来就是发给旧地址的，半路搬到另一个目标更危险。

### 密钥池与配额治理

一个下游可以挂 N 把密钥。挂上之后，`servers.headers` 就不再参与调用——
流量在密钥之间轮询，单把被限流不会让整个 server 不可用。

```
未配密钥  → 单密钥模式：一直用 servers.headers，行为与 M3 一字不差
配了密钥  → 密钥池模式：每把按状态与配额决定是否参与轮换
```

**轮换。** 原子游标顺时针找第一把可用者，即轮询。刻意不用"最少在飞"：
轮换的目的是**摊薄**，让每把都均匀用上，避免某把被打到限流甚至封禁；
按在飞数挑会退化成"谁快用谁"，反而集中。新加的密钥追加到顺序末尾。

**判决分两轴**，这是理解整套行为的关键：

| 轴 | 回答的问题 | 取值 |
|---|---|---|
| `ErrorKind` | 下游为什么失败（面向人） | `RATE_LIMITED` / `UPSTREAM_ERROR` / `TIMEOUT` … |
| `KeyVerdict` | 这把密钥还能不能用（面向调度器） | `OK` / `RATE_LIMITED` / `QUOTA_EXHAUSTED` / `INVALID_KEY` / `KEY_REVOKED` / `NEUTRAL` |

不合并成一个枚举：同一个 5xx，前者是 `UPSTREAM_ERROR`，后者是 `NEUTRAL`
（别动密钥）。合成一个，两边加值都会污染对方的语义。

**按判决处置**（`internal/downstream/keypool.go`）：

| 下游反应 | 判决 | 动作 |
|---|---|---|
| 2xx | `OK` | **唯一**能重置退避计数的信号 |
| 429（响应体是限流） | `RATE_LIMITED` | 该密钥冷却，退避 1s→2s→4s…上限 5min，±20% 抖动 |
| 429 声明 `Retry-After` | `RATE_LIMITED` | 冷却时长以下游声明为准（并在界面显示） |
| 402，或 429 且响应体提到 quota/billing | `QUOTA_EXHAUSTED` | 弃用（持久）——钱没了不会自己回来 |
| 401 | `INVALID_KEY` | 弃用（持久） |
| 403 | `KEY_REVOKED` | **连续 2 次**才弃用 |
| 5xx / 超时 / 传输错误 | `NEUTRAL` | 密钥状态**零变化**，也不重试 |

几个不显然但要紧的点：

- **冷却到期不重置退避计数**，只有一次成功才清零。否则退化成
  `1s → 重试 → 429 → 1s` 的死循环，退避永远升不上去。
- **403 给两次机会，401 不给。** 401 语义无歧义；403 也可能是 IP 白名单、
  scope 或地区封锁——换密钥没用，单次即弃用会把可用密钥烧光，还掩盖病因。
- **弃用是持久状态，冷却是内存状态。** 重启往往就是为了从那次限流里恢复，
  持久化限流只会让新进程背旧账；但"这把密钥无效"是稳定事实，重启后不该
  再拿它去打一次。
- **集体失败保护。** 弃用后会导致可用密钥归零、且其他密钥因同一原因失败时
  **不禁用**，转把 server 标成 `degraded`。挡的是"头名写错 / 账号被封 /
  IP 被拉黑"这类事故——否则网关会尽职尽责地把 20 把密钥逐个禁用，
  用户回来只看到一片红。
- **重试只对 401/402/403/429 开放**（确定未执行）。5xx、超时、业务错误可能
  **已经执行**；密钥池让重试变便宜了，但"可能重复下单"没有变便宜。
- **准入不排队。** 全部密钥配额用尽即快速失败并返回 `retryAfter`。排队会把
  延迟不确定性灌进网关，与 P50 < 1ms 的目标直接冲突。

**配额。** `rpmLimit` 精确可执行（固定窗口令牌桶，发请求前扣减）。
`tpmLimit` 是**尽力而为**：发请求前拿不到 token 数，按
「响应声明的用量 → 每工具预估 → 不计量」三级回退，欠账结转到下一个窗口。
取不到用量时界面显示**"未计量"而不是 `0 / 100k`**——一个假的 0 会让人
以为一切正常。

**日志归因。** 每条调用日志带 `credentialId`，密钥事件（冷却 / 弃用 / 恢复）
也进日志（`status=key`）。没有这一列就无法回答"这把密钥为什么失败了"。

**调用方视角**：限流、换密钥、冷却都发生在网关内部，AI 客户端只看到
"调用成功了"。只有当**池子里所有密钥同时不可用**时才会失败，那时返回
`503` + `Retry-After`，错误体里带各状态计数与最近一次原因。

**已知边界**：

- **stdIO 不适用。** 凭据在子进程环境变量里，改它必须重启进程，无法按调用
  轮换。对 stdio 配密钥会被入口直接拒绝（`400 key_pool_unsupported`），
  而不是"看起来配上了但不生效"。
- **多实例分摊不共享状态。** 配额计数与冷却都在进程内存里。跑两个实例时
  每个实例都会按自己的计数打下游，实际速率是配额 × 实例数——要么让实例
  各配一半配额，要么只跑一个实例。
- `rpmLimit` 是**每把密钥**的限制，不是 server 级。

### 错误分类

失败原因归入固定枚举，供界面与日志使用（见 `internal/downstream/kind.go`）：

```
DNS_FAIL  CONN_REFUSED  TLS_FAIL  TIMEOUT
AUTH_REQUIRED  NOT_FOUND  UPSTREAM_ERROR  NOT_MCP
PROTOCOL_MISMATCH  CAPABILITY_MISSING
SPAWN_FAILED  EXIT_EARLY  HANDSHAKE_TIMEOUT
SERVER_DRAINING  CANCELED
RATE_LIMITED  QUOTA_EXHAUSTED  NO_USABLE_KEY
```

最后五类不属于"下游坏了"：`SERVER_DRAINING` / `CANCELED` 是网关自己的意图
导致调用未完成（目标正在停用 / 收尾超时被强制中断）；`RATE_LIMITED` /
`QUOTA_EXHAUSTED` 是**密钥**的处境——单列是为了说明"问题在这把密钥上，
换一把就好"，而不是"整个下游挂了"；`NO_USABLE_KEY` 表示池子里一把能用的
都没有。混进 `UNKNOWN` 的话，用户只会看到一堆"未知错误"，无从把它和
几分钟前那次停用、或某个密钥被限流联系起来。

失败返回的文案按分类给不同建议：`NO_USABLE_KEY` / `RATE_LIMITED` 是
"稍后重试，不必换工具"，`QUOTA_EXHAUSTED` / `AUTH_REQUIRED` 是"重试无用，
去处理密钥"。**给错建议比不给更糟**——让一个被限流的调用去"换个工具"，
它会认定这些工具都不能用，转而放弃整条路径，而实际等几秒就恢复了。

`RATE_LIMITED` 与 `QUOTA_EXHAUSTED` 靠响应体区分（都可能是 429）：前者会
自己恢复，后者不会。判错方向的代价不对称——把额度耗尽当成限流，会让一把
没钱的密钥被无限退避重试。

分类全部由错误类型推导（`errors.Is` / `errors.As`），**不做字符串匹配**。

这带来一个约束：mcp-go 把 HTTP 状态码与内容类型压成了字符串错误，
所以网关在 `http.RoundTripper` 层插了一个 `Recorder`（`internal/downstream/recorder.go`），
在那里捕获结构化的状态码、`Content-Type`、`Retry-After`、响应体片段与
未包装的网络错误。

`Recorder` 的观测按**请求上下文**归位，而不是"最近一次往返"。这在单密钥
串行探活下看不出区别，但密钥池的调用是**并发**的：用共享槽位记录会把 A 收到
的 429 读到 B 头上，症状是**把一把健康的密钥封掉**。`TestKeyPoolConcurrentAttribution`
专门守这一条。

## 实测数据

环境：本机 macOS，单进程，无容器。`tools/list` 走内存快照，不触下游。

| 场景 | P50 | P95 | P99 |
|---|---:|---:|---:|
| `tools/list`（5 个工具，网关自身开销） | **0.18 ms** | 0.26 ms | 0.53 ms |
| `tools/call` → 本机 stdio 下游（常驻连接） | **0.29 ms** | 0.40 ms | 0.73 ms |

对照：ContextForge 的第三方压测结果是每次调用 **+23ms**。
设计目标是 P50 < 1ms，当前余量充足。

200 次连续调用 stdio 下游：**200 成功 / 0 失败**。

## 目录结构

```
cmd/mcp-hub/
  main.go                   CLI 入口与命令分发
  serve.go                  serve 命令：启动、周期探活、优雅关闭
  add.go                    add 命令：写配置 + 探活
internal/downstream/        下游适配与探活
  spec.go                   连接配置 + 连接指纹（决定何时必须重建）
  kind.go                   ErrorKind / KeyVerdict / ProbeResult / ToolInfo
  classify.go               错误分类
  recorder.go               RoundTripper 观测器（按请求 ctx 归位）
  network.go                L1 网络探测
  probe.go                  L1–L3 探活编排（一次性连接）
  conn.go                   常驻连接：动态凭据、优雅收尾、401 重试
  build.go                  按传输类型构造 client
  keypool.go                密钥池：轮换、状态机、退避、集体失败保护
  quota.go                  RPM 固定窗口桶 + TPM 三级回退与欠账结转
  usage.go                  从工具结果里抽取下游声明的 token 用量
  stderr.go                 stdio stderr 捕获
  testdata/echoserver/      测试用 stdio MCP server
  testdata/slowserver/      测试用"慢"stdio MCP server（验证停用收尾）
  testdata/gatedserver/     按凭据给不同答复的下游（手工验证密钥池）
internal/hub/               聚合核心
  registry.go               路由映射表 + 不可变聚合快照
  pool.go                   连接池：懒连接、失败退避、密钥选择与调用内重试
  aggregator.go             对外 MCP server：tools/list 聚合 + tools/call 路由
  service.go                编排：探活刷新、工具过滤、故障隔离、密钥编排、异步日志
internal/api/               管理后端
  api.go                    路由注册、鉴权、统一错误体
  servers.go                server 增删改、连通性测试、批量导入
  credentials.go            密钥增删改、批量粘贴、单密钥探活、脱敏
  query.go                  概览、工具浏览、日志查询、接入指引
  dto.go                    出入参结构与密钥打码
internal/httpd/
  server.go                 HTTP 层：/mcp、/sse、健康检查、中间件
internal/store/             SQLite 持久层
  schema.sql                servers / tools / call_logs / credentials
  store.go                  数据访问
  credential.go             密钥数据访问
```

## 开发

```bash
go vet ./...
go test ./... -count=1
```

测试用 `httptest` + mcp-go 内建 server 起**真实** MCP server，不 mock 协议；
stdio 路径会现场编译 `internal/downstream/testdata/echoserver` 作为 fixture。

聚合测试覆盖：前缀命名、路由不串线、参数与结果透传、
下游故障隔离、工具缓存保留、停用排除、工具过滤（含"过滤后不可调用"）、
调用日志、并发调用、stdio 跨请求存活。

生命周期测试（`graceful_test.go`）覆盖：改密钥不重建连接且不打断在飞调用、
401 在凭据轮换期间自动重试（含"凭据没变则不重试"这个反例）、
换目标必须重建连接、改显示名不重建、停用等在飞调用跑完、
超过等待上限强制关闭、没有在飞调用时停用立即完成。
判断"连接有没有被重建"用的是下游侧的握手计数与收到的凭据序列，
而不是断言网关内部字段——内部字段对不对不是重点，请求到底带出去什么才是。

管理接口测试覆盖：增删改查全流程、启停对 AI 可见工具列表的影响、
密钥打码与"提交 `***` 保留原值"、重复 key 409、配置校验 400、
`--api-key` 鉴权（含写操作与健康检查豁免）、`mcpServers` 批量导入、
失败下游不影响聚合端点、日志筛选与清空。

密钥池测试分两层。单元层（`downstream/keypool_test.go`）用假时钟精确控制
退避与冷却时刻：轮换均分、冷却跳过、退避递增且**成功后**才重置、
`Retry-After` 优先、401/403 阈值差异、全部冷却快速失败、5xx 零变化、
集体失败保护、RPM 溢出转下一把、重启语义、明文不泄漏。
端到端层（`hub/keypool_e2e_test.go`）用真实 HTTP 下游，按凭据给不同答复：
限流换密钥后调用照常成功、无效密钥被弃用且重载后仍不用、
池子被限流打空时快速失败、并发下**被冷却的恰好是收到 429 的那把**、
日志带 `credentialId`、单密钥模式行为不变。

密钥管理接口测试（`api/credentials_test.go`）：完整生命周期、明文绝不出现在
任何响应里（逐接口搜字节）、粘贴导入的自动命名与重名后缀、stdio 被拒、
密钥真的被用上（下游看得到轮换）、单密钥探活**只读**、
跨 server 操作密钥返回 404。

手工验证密钥池（真实进程 + curl）：

```bash
# 起一个"按凭据给不同答复"的下游：末尾 -rl → 429，末尾 -bad → 401
go run ./internal/downstream/testdata/gatedserver -addr 127.0.0.1:19999
```

```bash
go test ./... -count=1 -race   # 并发相关的改动必须跑这个
```

## 设计约定

- **零外部依赖**：不引入 Redis、独立 Postgres 或 Node 运行时。
- **单二进制**：前端产物最终用 `go:embed` 内嵌。
- **错误必须可分类**：禁止用字符串匹配判断错误。
- **探活与生产连接分离**：探活用一次性 client，用完即弃；
  调用走 `Pool` 里的常驻连接。
- **工具名冲突**：对外名 `<serverKey>__<toolName>`，但路由走显式映射表，
  不靠字符串切分（下游工具名本身可能含 `__`）。
- **下游不可控**：聚合的是第三方 server，其 schema 与注解不可信。
  MCP server 层开了 `WithRecovery`，任何一处 panic 只能影响那一个请求。
