// Package tests 存放 cmd/mcp-hub 的测试。
//
// main 包无法被导入，因此这里统一做黑盒测试：被测纯函数都抽到了可导入的
// internal 包（如 internal/httpd），本目录只从外部验证其行为。
package tests
