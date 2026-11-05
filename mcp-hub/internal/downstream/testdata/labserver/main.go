// Command labserver 是端到端实测用的可编程下游。
//
// 工具清单、故障开关、调用延迟全部从文件现读：改文件即改行为，不必重启进程。
//
// 工具调用按凭据后缀给不同答复：末尾 -rl → 429 + Retry-After（秒数可配）；
// -bad → 401；-pay → 402；-deny → 403；其它正常返回。
//
// 用法：
//
//	labserver -addr 127.0.0.1:19921 -tools /tmp/e2e/tools/lab1.json \
//	          -flags /tmp/e2e/tools/lab1.flags.json -retry-after 3
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	mcpserver "github.com/mark3labs/mcp-go/server"
)

type toolSpec struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	Result      string         `json:"result"`
	DelayMS     int            `json:"delayMs"`
}

// flags 是运行期的故障与时序开关，每次请求现读。
type flags struct {
	Down      bool   `json:"down"`      // 上游整体不可用
	ListError string `json:"listError"` // tools/list 返回的业务错误（空=正常）
	DelayMS   int    `json:"delayMs"`   // 每个响应前的延迟，用于在飞场景
}

var (
	toolsPath  string
	flagsPath  string
	retryAfter string
)

func loadTools() []toolSpec {
	b, err := os.ReadFile(toolsPath)
	if err != nil {
		return nil
	}
	var list []toolSpec
	if err := json.Unmarshal(b, &list); err != nil {
		return nil
	}
	return list
}

func loadFlags() flags {
	if flagsPath == "" {
		return flags{}
	}
	b, err := os.ReadFile(flagsPath)
	if err != nil {
		return flags{}
	}
	var f flags
	_ = json.Unmarshal(b, &f)
	return f
}

func main() {
	addr := flag.String("addr", "127.0.0.1:0", "监听地址")
	flag.StringVar(&toolsPath, "tools", "", "工具清单 JSON 文件路径（每次请求现读）")
	flag.StringVar(&flagsPath, "flags", "", "故障开关 JSON 文件路径（每次请求现读，可选）")
	flag.StringVar(&retryAfter, "retry-after", "3", "429 响应的 Retry-After（秒）")
	flag.Parse()

	// 只让 mcp-go 负责握手（initialize / notifications），工具清单与调用
	// 由下面的处理函数自己应答——这样才能做到"改文件即改清单"。
	srv := mcpserver.NewMCPServer("lab-downstream", "1.0.0",
		mcpserver.WithToolCapabilities(false))
	inner := mcpserver.NewStreamableHTTPServer(srv)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		raw := string(body)
		r.Body = io.NopCloser(strings.NewReader(raw))

		var req struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		_ = json.Unmarshal([]byte(raw), &req)

		f := loadFlags()
		if f.Down {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"message":"upstream temporarily down"}}`))
			fmt.Printf("[lab] %s -> 503 (down)\n", req.Method)
			return
		}
		if f.DelayMS > 0 {
			time.Sleep(time.Duration(f.DelayMS) * time.Millisecond)
		}

		switch req.Method {
		case "tools/list":
			if f.ListError != "" {
				writeRPC(w, req.ID, map[string]any{
					"content": []map[string]any{{"type": "text", "text": f.ListError}},
					"isError": true,
				})
				fmt.Printf("[lab] tools/list -> isError: %s\n", f.ListError)
				return
			}
			tools := []map[string]any{}
			for _, t := range loadTools() {
				schema := t.InputSchema
				if schema == nil {
					schema = map[string]any{"type": "object"}
				}
				tools = append(tools, map[string]any{
					"name":        t.Name,
					"description": t.Description,
					"inputSchema": schema,
				})
			}
			writeRPC(w, req.ID, map[string]any{"tools": tools})
			fmt.Printf("[lab] tools/list -> %d 个工具\n", len(tools))
			return

		case "tools/call":
			var call struct {
				Params struct {
					Name      string         `json:"name"`
					Arguments map[string]any `json:"arguments"`
				} `json:"params"`
			}
			_ = json.Unmarshal([]byte(raw), &call)

			// 单个工具也能自带延迟，用来卡在飞的时序。
			for _, t := range loadTools() {
				if t.Name == call.Params.Name && t.DelayMS > 0 {
					time.Sleep(time.Duration(t.DelayMS) * time.Millisecond)
					break
				}
			}

			auth := r.Header.Get("Authorization")
			switch {
			case strings.HasSuffix(auth, "-rl"):
				w.Header().Set("Retry-After", retryAfter)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"error":{"message":"rate limit reached"}}`))
				fmt.Printf("[lab] tools/call %s auth=%q -> 429\n", call.Params.Name, auth)
				return
			case strings.HasSuffix(auth, "-bad"):
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":{"message":"invalid api key"}}`))
				fmt.Printf("[lab] tools/call %s auth=%q -> 401\n", call.Params.Name, auth)
				return
			case strings.HasSuffix(auth, "-pay"):
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusPaymentRequired)
				_, _ = w.Write([]byte(`{"error":{"message":"quota exhausted"}}`))
				fmt.Printf("[lab] tools/call %s auth=%q -> 402\n", call.Params.Name, auth)
				return
			case strings.HasSuffix(auth, "-deny"):
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"error":{"message":"forbidden"}}`))
				fmt.Printf("[lab] tools/call %s auth=%q -> 403\n", call.Params.Name, auth)
				return
			}

			text := "ok:" + call.Params.Name
			for _, t := range loadTools() {
				if t.Name == call.Params.Name && t.Result != "" {
					text = t.Result
					break
				}
			}
			fmt.Printf("[lab] tools/call %s auth=%q -> ok\n", call.Params.Name, auth)
			writeRPC(w, req.ID, map[string]any{
				"content": []map[string]any{{"type": "text", "text": text}},
				"isError": false,
			})
			return
		}

		inner.ServeHTTP(w, r)
	})

	httpSrv := &http.Server{Addr: *addr, Handler: handler}
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatalf("监听失败: %v", err)
	}
	fmt.Printf("READY http://%s\n", ln.Addr().String())
	if err := httpSrv.Serve(ln); err != nil && err != http.ErrServerClosed {
		log.Fatalf("服务退出: %v", err)
	}
}

func writeRPC(w http.ResponseWriter, id any, result map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": "2.0", "id": id, "result": result,
	})
}
