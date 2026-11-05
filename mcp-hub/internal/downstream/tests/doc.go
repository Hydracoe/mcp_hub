// Package tests 存放 internal/downstream 的黑盒测试。
//
// 全部通过导出 API 驱动：真起 MCP server 或 HTTP 端点当下游，从结果与错误分类
// 上看行为，不 mock 协议、也不碰包内私有函数。
//
// 运行：go test ./internal/downstream/tests/ -count=1（密钥池并发用例需加 -race）
package tests
