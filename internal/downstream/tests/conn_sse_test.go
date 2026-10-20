package tests

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hydra/mcp-hub/internal/downstream"
	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

// TestConnSSEDialUsesCredentialFromContext 复现密钥池模式下的 SSE 握手。
//
// 密钥池按调用把凭据放在 ctx 上（WithCredentialHeaders），Conn 自身的 headers
// 为空。SSE 的 Start 就是握手：它用 Start 传入的 ctx 去取凭据。若 Start 只拿到
// 不带凭据的 lifeCtx，握手会裸奔并收到 401——而在池子看来，这 401 是"这把密钥
// 无效"，会把刚选中、本来有效的那把弃用。
func TestConnSSEDialUsesCredentialFromContext(t *testing.T) {
	mcpSrv := mcpserver.NewMCPServer("sse-fake", "1.0.0", mcpserver.WithToolCapabilities(false))
	mcpSrv.AddTool(mcp.NewTool("ping", mcp.WithDescription("ping")),
		func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return mcp.NewToolResultText("pong"), nil
		})

	sseSrv := mcpserver.NewSSEServer(mcpSrv)

	var rejected atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer pool-key" {
			rejected.Add(1)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		sseSrv.ServeHTTP(w, r)
	})
	httpSrv := httptest.NewServer(handler)
	t.Cleanup(httpSrv.Close)

	// 密钥池模式：配置里没有静态 headers，凭据只在本次 Dial 的 ctx 上。
	spec := downstream.Spec{
		Key:       "sse",
		Name:      "sse",
		Transport: downstream.TransportSSE,
		URL:       httpSrv.URL + "/sse",
		Timeout:   5 * time.Second,
	}
	conn, err := downstream.NewConn(spec)
	if err != nil {
		t.Fatalf("NewConn 失败: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	ctx := downstream.WithCredentialHeaders(context.Background(),
		map[string]string{"Authorization": "Bearer pool-key"})
	if _, err := conn.Dial(ctx); err != nil {
		t.Fatalf("SSE 握手没有带上本次调用的凭据: %v（被拒 %d 次）", err, rejected.Load())
	}
	if n := rejected.Load(); n != 0 {
		t.Errorf("下游拒绝了 %d 次握手：SSE 握手没有携带 ctx 上的凭据", n)
	}
}
