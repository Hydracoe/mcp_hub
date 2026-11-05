# internal/downstream

网关和下游 MCP server 之间的那一层。上游（`internal/hub`、`internal/api`、`cmd`）只跟它打交道，不直接碰 mcp-go；下游可能是 HTTP 端点，也可能是子进程。

## 分层

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

两条贯穿全包的原则：**失败必须归类**（上层要靠分类决定 HTTP 状态码、日志文案与给 AI 的说明，调用路径统一返回 `*CallError`）；**判定要用结构化信息**（状态码、`*net.DNSError` 被 mcp-go 压成字符串后取不回来，所以观测点放在 `RoundTripper`）。

## 两条主要路径

- **调用**：`Conn.CallTool` → 取并发额度 → 挂观测槽位 → 注入凭据（`WithCredentialHeaders` 优先，其次连接自己的）→ 失败则分类为 `CallError`。凭据 401 且期间被换过时重试一次。
- **探活**：`Prober.Probe` → L1 网络 → L2 协商（新版失败自动回退旧版）→ L3 `tools/list`，全程新建连接、用完即弃，不碰生产连接池。

## 测试

```bash
go test ./internal/downstream/tests/ -count=1
go test ./internal/downstream/tests/ -count=1 -race   # 密钥池并发用例需要它
```

全部用例为黑盒（`package tests`）。`testdata/` 留在原地：`internal/hub/tests/aggregate_test.go` 会以 `cmd.Dir = ../../downstream` 编译 `./testdata/echoserver`。

## 已知边界

- `TransportSSE` 仍有代码路径，但没有测试覆盖，实际也没在用。
- 密钥池的冷却与连续计数只在内存里，重启即丢；`disabled` 是持久态。
