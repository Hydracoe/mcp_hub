package httpd

import "net"

// IsUnbound 判断地址是否"还没 bind"：以冒号开头（如 :9090）或端口为 0（系统分配）。
// Start 之前 Server.Addr() 处于这两种状态，调用方据此判断能否对外展示地址。
func IsUnbound(addr string) bool {
	if addr == "" {
		return true
	}
	if addr[0] == ':' {
		return true
	}
	_, port, err := net.SplitHostPort(addr)
	return err == nil && port == "0"
}

// NormalizeBase 把监听地址折成可直接粘贴的 URL。
func NormalizeBase(addr string) string {
	if len(addr) > 0 && addr[0] == ':' {
		return "http://127.0.0.1" + addr
	}
	// [::]:9090 → 127.0.0.1:9090
	if len(addr) > 4 && addr[:5] == "[::]:" {
		return "http://127.0.0.1:" + addr[5:]
	}
	if len(addr) > 0 && addr[0] == '[' {
		return "http://" + addr
	}
	return "http://" + addr
}
