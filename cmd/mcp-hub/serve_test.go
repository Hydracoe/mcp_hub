package main

import "testing"

// 这两个函数决定启动横幅打印什么 URL。用户实际会照着它粘贴到客户端配置里，
// 所以错了不是"显示不好看"，而是"照抄一个连不上的地址"。
func TestIsUnbound(t *testing.T) {
	cases := []struct {
		addr string
		want bool
	}{
		{"", true},                 // Start 之前 Addr() 可能为空
		{":9090", true},            // 配置值原样返回，尚未绑定
		{"127.0.0.1:0", true},      // 端口写 0：真实端口要等 bind
		{"[::]:0", true},           // 同上，IPv6 形态
		{"127.0.0.1:65483", false}, // 已绑定
		{"[::]:65483", false},      // 已绑定
		{"0.0.0.0:9090", false},    // 已绑定（不推荐，但它确实是绑定了）
		{"127.0.0.1:9090", false},  // 已绑定
	}
	for _, c := range cases {
		if got := isUnbound(c.addr); got != c.want {
			t.Errorf("isUnbound(%q) = %v, want %v", c.addr, got, c.want)
		}
	}
}

func TestNormalizeBase(t *testing.T) {
	cases := []struct {
		addr string
		want string
	}{
		{":9090", "http://127.0.0.1:9090"},
		{"127.0.0.1:65483", "http://127.0.0.1:65483"},
		{"[::]:9090", "http://127.0.0.1:9090"}, // 通配地址换成可点开的回环
		{"0.0.0.0:9090", "http://0.0.0.0:9090"},
	}
	for _, c := range cases {
		if got := normalizeBase(c.addr); got != c.want {
			t.Errorf("normalizeBase(%q) = %q, want %q", c.addr, got, c.want)
		}
	}
}
