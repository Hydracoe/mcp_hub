package pool

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hydra/mcp-hub/internal/downstream"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// testNow 是固定的"此刻"：退避窗口的断言必须是确定性的，
// 不靠 sleep 抢时间，也不受机器快慢影响。
func testNow() time.Time {
	return time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
}

// newTestPool 造一个时间可控的连接池。
func newTestPool(t *testing.T) *Pool {
	t.Helper()
	p := NewPool(nil)
	now := testNow()
	p.now = func() time.Time { return now }
	return p
}

// closedEndpoint 返回一个确定连不上的地址：先占一个端口再放开，
// 拨号必然被拒。不需要 sleep，也不存在"还没放开的那个窗口"。
func closedEndpoint(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("占用端口失败: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("释放端口失败: %v", err)
	}
	return "http://" + addr
}

// unreachableSpec 造一个指向"连不上"地址的 spec。
func unreachableSpec(t *testing.T, id string) downstream.Spec {
	t.Helper()
	return downstream.Spec{
		ID:        id,
		Key:       "demo",
		Transport: downstream.TransportStreamableHTTP,
		URL:       closedEndpoint(t),
		Timeout:   2 * time.Second,
	}
}

// 一次拨号失败只记一次账：Get 与外层若各记一次，同一次失败就被算成两次，
// 退避窗口会翻倍（2s→4s→8s），可用密钥被过快淘汰。
// 这里断言内部计数为 1，退避窗口停在第一档。
func TestDialFailureCountedOnce(t *testing.T) {
	p := newTestPool(t)
	spec := unreachableSpec(t, "srv-dial-1")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := p.CallTool(ctx, spec, "ping", json.RawMessage(`{}`)); err == nil {
		t.Fatal("拨号失败时 CallTool 应当返回错误")
	}

	count, until, ok := p.Failure(spec.ID)
	if !ok {
		t.Fatal("拨号失败应当留下一条失败记录")
	}
	if count != 1 {
		t.Fatalf("一次拨号失败被计了 %d 次, want 1（同一次失败被记两次，退避窗口会翻倍）", count)
	}
	// 退避档位由计数决定：count=1 对应第一档。记成两次就会跳到第二档。
	if until.IsZero() {
		t.Fatal("失败没有推进退避窗口")
	}
	if want := testNow().Add(backoffInitial); !until.Equal(want) {
		t.Fatalf("退避窗口 = %s, want %s（count=%d 决定的档位）",
			until.Sub(testNow()), backoffInitial, count)
	}
}

// 密钥池模式下同样只记一次：判决按错误算出（不是写死的 NEUTRAL），
// 但记账仍发生在 Get 与外层两处。
func TestDialFailureCountedOnceWithKeyRing(t *testing.T) {
	p := newTestPool(t)
	spec := unreachableSpec(t, "srv-dial-2")

	p.SyncKeyRing(spec.ID, downstream.KeyRingOptions{
		ServerID:    spec.ID,
		ServerKey:   spec.Key,
		JitterRatio: -1, // 关掉抖动：测试要的是确定性
		Now:         p.now,
	}, []downstream.CredentialInput{
		{ID: "key-1", Label: "密钥 1", Headers: map[string]string{"Authorization": "Bearer s1"},
			Enabled: true, State: downstream.KeyActive},
		{ID: "key-2", Label: "密钥 2", Headers: map[string]string{"Authorization": "Bearer s2"},
			Enabled: true, State: downstream.KeyActive},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := p.CallTool(ctx, spec, "ping", json.RawMessage(`{}`)); err == nil {
		t.Fatal("拨号失败时 CallTool 应当返回错误")
	}

	count, _, ok := p.Failure(spec.ID)
	if !ok {
		t.Fatal("拨号失败应当留下一条失败记录")
	}
	if count != 1 {
		t.Fatalf("一次拨号失败被计了 %d 次, want 1", count)
	}
}

// 调用阶段的失败仍然要记一次账：拨号失败归 Get 记，调用失败归外层记，
// 两条路径不能互相吞掉。
func TestCallFailureCountedOnce(t *testing.T) {
	p := newTestPool(t)

	srv := server.NewMCPServer("fake-mcp", "1.0.0", server.WithToolCapabilities(true))
	srv.AddTool(mcp.NewTool("boom"), func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return nil, errors.New("工具故意失败")
	})
	ts := httptest.NewServer(server.NewStreamableHTTPServer(srv))
	t.Cleanup(ts.Close)

	spec := downstream.Spec{
		ID:        "srv-call-1",
		Key:       "demo",
		Transport: downstream.TransportStreamableHTTP,
		URL:       ts.URL,
		Timeout:   5 * time.Second,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err := p.CallTool(ctx, spec, "boom", json.RawMessage(`{}`)); err == nil {
		t.Fatal("工具失败时 CallTool 应当返回错误")
	}

	count, _, ok := p.Failure(spec.ID)
	if !ok {
		t.Fatal("调用失败应当留下一条失败记录")
	}
	if count != 1 {
		t.Fatalf("一次调用失败被计了 %d 次, want 1", count)
	}
}

// 密钥级失败不该让整个 server 退避：握手就回 429 是"这把密钥被限流"，
// 换一把就过去，server 的失败计数不该动——只冷却密钥。
func TestKeyLevelFailureDoesNotBackOffServer(t *testing.T) {
	p := newTestPool(t)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	t.Cleanup(ts.Close)

	spec := downstream.Spec{
		ID:        "srv-key-1",
		Key:       "demo",
		Transport: downstream.TransportStreamableHTTP,
		URL:       ts.URL,
		Timeout:   5 * time.Second,
	}

	p.SyncKeyRing(spec.ID, downstream.KeyRingOptions{
		ServerID:    spec.ID,
		ServerKey:   spec.Key,
		JitterRatio: -1,
		Now:         p.now,
	}, []downstream.CredentialInput{
		{ID: "key-1", Label: "密钥 1", Headers: map[string]string{"Authorization": "Bearer s1"},
			Enabled: true, State: downstream.KeyActive},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err := p.CallTool(ctx, spec, "ping", json.RawMessage(`{}`)); err == nil {
		t.Fatal("下游限流时 CallTool 应当返回错误")
	}

	if count, _, ok := p.Failure(spec.ID); ok {
		t.Fatalf("密钥级失败不该推进 server 的失败计数，实际记了 %d 次", count)
	}

	// 密钥自己被冷却了：这才是密钥级失败该有的副作用。
	statuses := p.KeyStatusesOf(spec.ID)
	if len(statuses) != 1 {
		t.Fatalf("密钥状态数 = %d, want 1", len(statuses))
	}
	if statuses[0].State != downstream.KeyCooling {
		t.Fatalf("密钥状态 = %s, want %s（被限流的密钥该进冷却）",
			statuses[0].State, downstream.KeyCooling)
	}
}
