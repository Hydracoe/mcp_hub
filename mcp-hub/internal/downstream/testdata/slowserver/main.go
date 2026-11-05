// Command slowserver 是一个 MCP stdio server，只提供一个会挂很久的工具。
//
// 存在的理由：验证"停用要等在飞调用收尾"这条路径，需要一个能被拖住的
// 工具——真实下游做不到，它们该多快就多快。停留时长由环境变量
// SLOW_SECONDS 控制，默认 5 秒，便于把测试压在可接受的时长内。
//
// 用法（端到端手工验证）：
//
//	go build -o /tmp/slowserver ./internal/downstream/testdata/slowserver
//	SLOW_SECONDS=10 /tmp/slowserver
package main

import (
	"context"
	"os"
	"strconv"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// defaultDwell 是工具的默认停留时长。
const defaultDwell = 5 * time.Second

func main() {
	dwell := defaultDwell
	if v := os.Getenv("SLOW_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			dwell = time.Duration(n) * time.Second
		}
	}

	s := server.NewMCPServer("slowserver", "0.1.0", server.WithToolCapabilities(false))
	s.AddTool(
		mcp.NewTool("slow", mcp.WithDescription("Returns only after a long delay")),
		func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			select {
			case <-time.After(dwell):
				return mcp.NewToolResultText("finally"), nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
	)

	if err := server.ServeStdio(s); err != nil {
		panic(err)
	}
}
