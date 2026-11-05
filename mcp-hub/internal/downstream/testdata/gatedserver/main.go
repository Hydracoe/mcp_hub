// gatedserver 是按凭据决定是否放行的测试下游，用于复现密钥池的轮换、限流与弃用场景。
//
// 只拦工具调用，握手与工具列举一律放行，否则一次 401 会把工具缓存一起打掉。
//
// 策略（按 Authorization 头）：末尾 -rl → 429 + Retry-After: 3；末尾 -bad → 401；
// 其它 → 正常返回。
//
// 用法：
//
//	go run ./internal/downstream/testdata/gatedserver -addr 127.0.0.1:19999
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:0", "监听地址")
	flag.Parse()

	srv := mcpserver.NewMCPServer("gated-downstream", "1.0.0",
		mcpserver.WithToolCapabilities(false))
	srv.AddTool(
		mcp.NewTool("ping", mcp.WithDescription("returns pong")),
		func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return mcp.NewToolResultText("pong"), nil
		},
	)

	inner := mcpserver.NewStreamableHTTPServer(srv)

	httpSrv := &http.Server{Addr: *addr, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")

		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(strings.NewReader(string(body)))

		if strings.Contains(string(body), `"method":"tools/call"`) {
			switch {
			case strings.HasSuffix(auth, "-rl"):
				w.Header().Set("Retry-After", "3")
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"error":{"message":"rate limit reached"}}`))
				return
			case strings.HasSuffix(auth, "-bad"):
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":{"message":"invalid api key"}}`))
				return
			}
			fmt.Printf("[downstream] tools/call auth=%q → ok\n", auth)
		}

		inner.ServeHTTP(w, r)
	})}

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatalf("监听失败: %v", err)
	}
	fmt.Printf("READY http://%s\n", ln.Addr().String())
	if err := httpSrv.Serve(ln); err != nil && err != http.ErrServerClosed {
		log.Fatalf("服务退出: %v", err)
	}
}
