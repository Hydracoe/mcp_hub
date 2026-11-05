package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hydra/mcp-hub/internal/downstream"
	"github.com/hydra/mcp-hub/internal/store/apikeys"
	"github.com/hydra/mcp-hub/internal/store/logs"
)

// postMCP 向 /mcp 端点发一条原始 JSON-RPC，返回状态码与响应体。
//
// 不走 MCP 客户端：401 与否发生在中间件，比协议层早得多，
// 用原始请求恰好只测鉴权这一层。
func postMCP(t *testing.T, base, key string, payload map[string]any) (int, string) {
	t.Helper()

	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("序列化请求失败: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, base+"/mcp", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if key != "" {
		req.Header.Set("X-API-Key", key)
	}

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("请求 /mcp 失败: %v", err)
	}
	defer res.Body.Close()

	var buf bytes.Buffer
	_, _ = buf.ReadFrom(res.Body)
	return res.StatusCode, buf.String()
}

func initializePayload() map[string]any {
	return map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-03-26",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "t", "version": "0"},
		},
	}
}

// callToolPayload 构造一次工具调用的 JSON-RPC。
func callToolPayload(name, args string) map[string]any {
	return map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "tools/call",
		"params": map[string]any{
			"name":      name,
			"arguments": json.RawMessage(args),
		},
	}
}

// ---- 接入 API key：鉴权与调用日志 --------------------------------------------

func TestAPIKeyAuthAndCallLogging(t *testing.T) {
	down := startDownstream(t, toolSpec{name: "echo", desc: "Repeat the given text"})
	e := newMetaEnv(t, []downstream.Spec{remoteSpec("echo", down)})
	ctx := context.Background()

	// 零 key：匿名放行（本地零配置体验不受影响）
	if code, _ := postMCP(t, e.ts.URL, "", initializePayload()); code != http.StatusOK {
		t.Fatalf("未创建 key 前匿名应放行，got %d", code)
	}

	// 创建第一把 key = 打开鉴权开关
	key, plaintext, err := e.store.APIKeys.Create(ctx, "claude-desktop")
	if err != nil {
		t.Fatalf("创建 api key 失败: %v", err)
	}
	if !strings.HasPrefix(plaintext, apikeys.KeyPrefix) {
		t.Fatalf("明文应带前缀: %q", plaintext)
	}

	// 匿名 / 错 key → 401；对 key → 200
	if code, _ := postMCP(t, e.ts.URL, "", initializePayload()); code != http.StatusUnauthorized {
		t.Fatalf("创建 key 后匿名应 401，got %d", code)
	}
	if code, _ := postMCP(t, e.ts.URL, "mh-deadbeef", initializePayload()); code != http.StatusUnauthorized {
		t.Fatalf("错误 key 应 401，got %d", code)
	}
	if code, _ := postMCP(t, e.ts.URL, plaintext, initializePayload()); code != http.StatusOK {
		t.Fatalf("正确 key 应放行，got %d", code)
	}

	// 带 key 调用工具，日志必须归因到这把 key 并记录参数与结果
	code, body := postMCP(t, e.ts.URL, plaintext, callToolPayload("echo__echo", `{"text":"hi"}`))
	if code != http.StatusOK || strings.Contains(body, `"isError":true`) {
		t.Fatalf("带 key 调用工具失败: %d %s", code, body)
	}

	// 日志异步落库（500ms 批量），轮询等待
	deadline := time.Now().Add(5 * time.Second)
	var entries []logs.LogEntry
	for {
		entries, _, err = e.store.Logs.List(ctx, logs.LogQuery{})
		if err != nil {
			t.Fatalf("查询日志失败: %v", err)
		}
		if len(entries) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("等待调用日志落库超时")
		}
		time.Sleep(100 * time.Millisecond)
	}

	rec := entries[0]
	if rec.APIKeyID != key.ID {
		t.Errorf("日志应归因到调用方 key: got %q want %q", rec.APIKeyID, key.ID)
	}
	// 名字快照必须跟 id 一起落到日志里：只给界面一个 32 位 hex，
	// 用户仍然答不出"这是哪个客户端"。
	if rec.APIKeyName != key.Name {
		t.Errorf("日志应记录调用方名字快照: got %q want %q", rec.APIKeyName, key.Name)
	}
	if !strings.Contains(rec.Args, `"text":"hi"`) {
		t.Errorf("日志应记录入参: %q", rec.Args)
	}
	if rec.Result == "" {
		t.Error("日志应记录返回结果")
	}

	// 停用 → 立即 401（停用是收紧，不是放开匿名）
	if err := e.store.APIKeys.SetEnabled(ctx, key.ID, false); err != nil {
		t.Fatalf("停用 key 失败: %v", err)
	}
	if code, _ := postMCP(t, e.ts.URL, plaintext, initializePayload()); code != http.StatusUnauthorized {
		t.Fatalf("停用后应 401，got %d", code)
	}

	// 删除全部 key → 回到匿名放行
	if err := e.store.APIKeys.Delete(ctx, key.ID); err != nil {
		t.Fatalf("删除 key 失败: %v", err)
	}
	if code, _ := postMCP(t, e.ts.URL, "", initializePayload()); code != http.StatusOK {
		t.Fatalf("删除全部 key 后匿名应放行，got %d", code)
	}
}
