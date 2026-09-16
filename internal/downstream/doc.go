// Package downstream 是网关与下游 MCP server 之间的那一层。
//
// 自下而上分五层，文件名即层次：
//
//	配置    spec.go       Spec 与它的校验、连接指纹
//	传输    build.go      按 transport 拼出 mcp-go client
//	        recorder.go   HTTP 观测：状态码、Retry-After、响应体片段
//	        network.go    L1 可达性探测（TCP/TLS）
//	        stderr.go     stdio 子进程 stderr 捕获
//	判定    kind.go       分类枚举与探活结果类型
//	        classify.go   失败 → ErrorKind
//	        errors.go     对外错误类型（CallError 等）
//	连接    conn.go       常驻连接：调用转发、动态凭据
//	        conn_lifecycle.go  并发额度、在飞取消、优雅停用
//	        probe.go      一次性探活（Prober）
//	调度    keyverdict.go       判决词汇：KeyState / KeyVerdict
//	        keyring.go          密钥池：结构、同步、选密钥
//	        keyring_settle.go   结果结算、退避、弃用
//	        keyring_status.go   状态快照与运维操作
//	        quota.go            RPM 窗口计数
//
// 两条原则贯穿全包：
//
//   - 失败必须归类。上层要靠分类决定 HTTP 状态码、日志文案与给 AI 的说明，
//     裸 error 做不到，所以调用路径统一返回 *CallError。
//   - 判定要用结构化信息。状态码、*net.DNSError 在传输层还看得见，被 mcp-go
//     压成字符串后就取不回来，所以观测点放在 RoundTripper（见 Recorder）。
//
// 测试全在 tests/ 子目录，只使用本包的导出 API。
package downstream
