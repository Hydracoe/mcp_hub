// Package tests 存放 internal/hub 的黑盒集成测试：通过导出 API 驱动 hub，
// 起真实 Service 与假下游/真 MCP server，走 HTTP 端点验证对外行为。
// 依赖真实 PostgreSQL（storetest 临时库）与 pgvector；运行：go test ./internal/hub/tests/ -count=1
package tests
