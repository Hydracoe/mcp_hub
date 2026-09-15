package downstream

import "testing"

func TestSpecAuthMode(t *testing.T) {
	base := Spec{
		Key:       "wx",
		Transport: TransportStreamableHTTP,
		URL:       "https://example.com/mcp",
	}

	// 非法值拦截
	bad := base
	bad.AuthMode = "secret"
	if err := bad.Validate(); err == nil {
		t.Fatal("未知 authMode 应报错")
	}

	// stdio 没有"请求头"概念
	stdio := Spec{Key: "local", Transport: TransportStdio, Command: "uvx", AuthMode: AuthModeHeader}
	if err := stdio.Validate(); err == nil {
		t.Fatal("stdio + authMode=header 应报错")
	}

	// 推断：配了头 = header，否则 none
	withHeaders := base
	withHeaders.Headers = map[string]string{"Authorization": "Bearer t"}
	if got := withHeaders.AuthModeOr(); got != AuthModeHeader {
		t.Errorf("配了头未声明时推断 = %q, want header", got)
	}
	if got := base.AuthModeOr(); got != AuthModeNone {
		t.Errorf("未配头未声明时推断 = %q, want none", got)
	}

	// 显式声明优先于推断：需要鉴权但还没配密钥，也是合法配置
	needsAuth := base
	needsAuth.AuthMode = AuthModeHeader
	if got := needsAuth.AuthModeOr(); got != AuthModeHeader {
		t.Errorf("显式声明 = %q, want header", got)
	}
	if err := needsAuth.Validate(); err != nil {
		t.Fatalf("header 模式（尚未配密钥）应可保存: %v", err)
	}
}
