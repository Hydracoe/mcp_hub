// Package tests 存放 internal/downstream 的黑盒测试。
//
// 全部通过导出 API 驱动：真起 MCP server 或 HTTP 端点当下游，再从结果与错误
// 分类上看行为，不 mock 协议、也不碰包内私有函数。放独立目录而非包内白盒测试，
// 是为了让测试与实现的文件划分解耦——downstream 内部怎么拆文件都不影响这里。
//
// 运行：
//
//	go test ./internal/downstream/tests/ -count=1
//	go test ./internal/downstream/tests/ -count=1 -race   # 密钥池并发用例需要它
package tests
