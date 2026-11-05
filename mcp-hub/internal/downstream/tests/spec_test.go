package tests

import (
	"testing"

	"github.com/hydra/mcp-hub/internal/downstream"
)

func TestSpecAuthMode(t *testing.T) {
	base := downstream.Spec{
		Key:       "wx",
		Transport: downstream.TransportStreamableHTTP,
		URL:       "https://example.com/mcp",
	}

	// 非法值拦截
	bad := base
	bad.AuthMode = "secret"
	if err := bad.Validate(); err == nil {
		t.Fatal("未知 authMode 应报错")
	}

	// stdio 没有"请求头"概念
	stdio := downstream.Spec{Key: "local", Transport: downstream.TransportStdio, Command: "uvx", AuthMode: downstream.AuthModeHeader}
	if err := stdio.Validate(); err == nil {
		t.Fatal("stdio + authMode=header 应报错")
	}

	// 推断：配了头 = header，否则 none
	withHeaders := base
	withHeaders.Headers = map[string]string{"Authorization": "Bearer t"}
	if got := withHeaders.AuthModeOr(); got != downstream.AuthModeHeader {
		t.Errorf("配了头未声明时推断 = %q, want header", got)
	}
	if got := base.AuthModeOr(); got != downstream.AuthModeNone {
		t.Errorf("未配头未声明时推断 = %q, want none", got)
	}

	// 显式声明优先于推断：需要鉴权但还没配密钥，也是合法配置
	needsAuth := base
	needsAuth.AuthMode = downstream.AuthModeHeader
	if got := needsAuth.AuthModeOr(); got != downstream.AuthModeHeader {
		t.Errorf("显式声明 = %q, want header", got)
	}
	if err := needsAuth.Validate(); err != nil {
		t.Fatalf("header 模式（尚未配密钥）应可保存: %v", err)
	}
}

func TestSpecValidateRejectsBadConfig(t *testing.T) {
	cases := []struct {
		name string
		spec downstream.Spec
	}{
		{"缺 key", downstream.Spec{Transport: downstream.TransportStreamableHTTP, URL: "http://x/mcp"}},
		{"key 含非法字符", downstream.Spec{Key: "a__b", Transport: downstream.TransportStreamableHTTP, URL: "http://x/mcp"}},
		{"远程缺 url", downstream.Spec{Key: "a", Transport: downstream.TransportStreamableHTTP}},
		{"url 无 host", downstream.Spec{Key: "a", Transport: downstream.TransportStreamableHTTP, URL: "http:///mcp"}},
		{"url 协议不对", downstream.Spec{Key: "a", Transport: downstream.TransportStreamableHTTP, URL: "ftp://x/mcp"}},
		{"stdio 缺 command", downstream.Spec{Key: "a", Transport: downstream.TransportStdio}},
		{"未知传输", downstream.Spec{Key: "a", Transport: "grpc"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.spec.Validate(); err == nil {
				t.Fatal("非法配置应当被拒绝")
			}
		})
	}
}
