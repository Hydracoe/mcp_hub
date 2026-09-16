# internal/downstream

网关和下游 MCP server 之间的那一层。上游（`internal/hub`、`internal/api`、`cmd`）只跟它打交道，
不直接碰 mcp-go；下游可能是一个 HTTP 端点，也可能是一个子进程。

## 分层

文件按层排，名字就是层次：

| 层 | 文件 | 管什么 |
| --- | --- | --- |
| 配置 | `spec.go` | `Spec`（连接配置）、校验、连接指纹 |
| 传输 | `build.go` | 按 transport 拼出 mcp-go client，启动交给调用方 |
| | `recorder.go` | HTTP 观测器：状态码、Retry-After、响应体片段 |
| | `network.go` | L1 可达性探测（DNS → TCP → TLS），不发 HTTP |
| | `stderr.go` | stdio 子进程 stderr 的尾部缓冲 |
| 判定 | `kind.go` | 分类枚举（`ErrorKind`）、`ProbeResult`、`ToolInfo` |
| | `classify.go` | 失败 → `ErrorKind` 的判定规则 |
| | `errors.go` | 对外错误类型：`CallError` 及其取值函数 |
| 连接 | `conn.go` | 常驻连接：转调用、动态凭据、401 重试 |
| | `conn_lifecycle.go` | 并发额度、在飞取消、`Drain` / `Close` |
| | `probe.go` | 一次性探活（`Prober`），与 Conn 共用协商路径 |
| 调度 | `keyverdict.go` | 判决词汇：`KeyState`、`KeyVerdict`、`VerdictOf` |
| | `keyring.go` | 密钥池：结构、同步、选密钥 |
| | `keyring_settle.go` | 结算判决：退避、弃用、集体失败保护 |
| | `keyring_status.go` | 状态快照、指纹、人工干预 |
| | `quota.go` | RPM 的固定窗口计数 |

`testdata/` 是 stdio 测试用的下游 fixture，`tests/` 是全部测试。

两条贯穿全包的原则：

1. **失败必须归类。** 上层要靠分类决定 HTTP 状态码、日志文案和给 AI 的说明，裸 `error` 做不到，
   所以调用路径统一返回 `*CallError`。
2. **判定要用结构化信息。** 状态码、`*net.DNSError` 在传输层还看得见，被 mcp-go 压成字符串后
   就取不回来，所以观测点放在 `RoundTripper`（`Recorder`）。

## 两条主要路径

- **调用**：`Conn.CallTool` → 取并发额度 → 挂观测槽位 → 注入凭据（`WithCredentialHeaders` 优先，其次连接自己的）
  → 失败则分类为 `CallError`。凭据 401 且期间被换过时重试一次。
- **探活**：`Prober.Probe` → L1 网络 → L2 协商（新版失败自动回退旧版）→ L3 `tools/list`，
  全程新建连接、用完即弃，不碰生产连接池。

## 本次重构做了什么

**文件重排。** 原来是 12 个平铺文件，其中 `keypool.go` 一个文件 959 行。现在按上表分层：
`conn.go` 拆出生命周期相关部分；`keypool.go` 按"判决词汇 / 选谁 / 用完怎么结算 / 怎么展示和干预"拆成四个；
`doc.go` 装包级分层说明；`errors.go` 单独放对外错误类型。

**去重。** "按传输类型选分类器"原来散了四份（`Conn.classify`、`Conn.classifyObs`、`Prober.classify`、
`classifyFor`），合并成一个 `classifyFor(spec, err, obs)`。工具定义转换（`ToolsToInfo` 与探活里的手写循环）
和 `marshalSchema` 都只留一份，放在 `kind.go` 里紧挨类型定义。

**删死代码**（全仓 grep 无调用点，需要时去 git 历史取）：

- `Conn.recorder()`、`Conn.OpenedAt()`（连带 `openedAt` 字段）、`Conn.Negotiated()`、`Conn.IsDraining()`
- `ObservationSlot.Captured()`、`Recorder.Reset()`
- `keyEntry.retryIn()`、`quotaWindow.nextRefill()`
- `KeyRing.EstTokensPerCall()`、`KeyStatus.LockedUntil`

**测试搬到 `tests/`。** 与 `internal/hub/tests`、`internal/store/tests` 保持一致：`package tests`、
只走导出 API。原来有两个用例直接调私有函数，改成更真实的路径：

- `parseRetryAfter` 的单测 → 从 `Recorder.Last()` 的 `RetryAfter` 断言（顺手覆盖了 body 不被偷吃、SSE 不偷看）。
- `classify` 的限流/额度区分 → 从探活结果的 `Kind` 断言，即真起一个回 429 的端点跑完整链路。

`testdata/` 留在原地没动：`internal/hub/tests/aggregate_test.go` 会 `cmd.Dir = ../../downstream`
编译 `./testdata/echoserver`，挪了会连带改别的包。

**注释精简。** 每个导出符号 1–2 行，只讲"为什么"；实现层注释从 641 行降到 548 行（占比 20% → 18%，
还包含 `doc.go` 那 30 行包说明），8 行以上的注释块从 13 个降到 0 个，最长块 16 行 → 5 行。
保留完整解释的只剩真的反直觉的地方：401 重试的前提、集体失败保护、租约为什么要持快照、
冷却到期为什么不重置退避。

## 跑测试

29 个用例，全部黑盒：

```bash
go test ./internal/downstream/tests/ -count=1
go test ./internal/downstream/tests/ -count=1 -race   # 密钥池并发用例需要它
```

## 已知边界（本次未改）

- `TransportSSE` 仍有代码路径，但没有测试覆盖，实际也没在用。
- 密钥池的冷却与连续计数只在内存里，重启即丢；`disabled` 是持久态。这是刻意的，有测试守着。
