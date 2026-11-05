package tests

import (
	"testing"
	"time"

	"github.com/hydra/mcp-hub/internal/downstream"
	"github.com/hydra/mcp-hub/internal/store"
	"github.com/hydra/mcp-hub/internal/store/storetest"
)

// newTestStore 每次起一个全新的 PostgreSQL 临时库。
//
// 测试跟着生产跑在同一个引擎上，"测试过了但线上炸"才没有缝。
// 库由 storetest 建，用例结束即 DROP。
func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(storetest.DSN(t))
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func testSpec() downstream.Spec {
	return downstream.Spec{
		Key:       "fake",
		Name:      "Fake MCP",
		Transport: downstream.TransportStreamableHTTP,
		URL:       "http://127.0.0.1:1234/mcp",
		Headers:   map[string]string{"Authorization": "Bearer t"},
		Timeout:   5 * time.Second,
	}
}

func okProbe() *downstream.ProbeResult {
	return &downstream.ProbeResult{
		OK:              true,
		ProtocolVersion: "2026-07-28",
		ServerName:      "fake-mcp",
		ServerVersion:   "1.2.3",
		Capabilities:    []string{"tools"},
		ToolCount:       2,
		Tools: []downstream.ToolInfo{
			{Name: "alpha", Description: "first"},
			{Name: "beta", Description: "second"},
		},
		Latency:   downstream.StageLatency{Total: 42},
		CheckedAt: time.Now(),
	}
}
