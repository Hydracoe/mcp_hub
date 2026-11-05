// Command echoserver 是一个最小的 MCP stdio server，
// 仅用于下游探活的集成测试（见 probe_test.go）。
package main

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func main() {
	s := server.NewMCPServer("echoserver", "0.1.0", server.WithToolCapabilities(false))

	s.AddTool(
		mcp.NewTool("echo", mcp.WithDescription("Echo the input back")),
		func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return mcp.NewToolResultText("echo"), nil
		},
	)
	s.AddTool(
		mcp.NewTool("now", mcp.WithDescription("Return a fixed timestamp")),
		func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return mcp.NewToolResultText("2026-09-14T00:00:00Z"), nil
		},
	)

	if err := server.ServeStdio(s); err != nil {
		panic(err)
	}
}
