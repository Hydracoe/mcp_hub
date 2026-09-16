// Package tests 存放 internal/hub 的黑盒集成测试。
//
// 测试全部通过导出 API 驱动 hub：起一个真实的 Service、把假下游或
// 真 MCP server 接上去，再走 HTTP 端点验证聚合、路由、密钥池与
// 优雅退场等对外行为。放独立目录而非包内白盒测试，是为了让测试与
// 实现的文件划分解耦——internal/hub 内部怎么拆文件都不影响这里。
//
// 依赖真实 PostgreSQL（由 storetest 创建临时库）与 docker compose 起的
// pgvector；需要 embedding 的用例用内置的假模型服务替代真实模型。
//
// 运行：
//
//	go test ./internal/hub/tests/ -count=1
package tests
