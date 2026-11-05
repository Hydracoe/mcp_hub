package httpd

import (
	"testing"

	"github.com/gin-gonic/gin"
)

// 配置值到 gin 模式名的映射：没配必须是 release。
// gin 自己的默认是 debug，会把中间件提示打到 stdout，把启动横幅淹掉。
func TestResolveGinMode(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", gin.ReleaseMode},
		{gin.ReleaseMode, gin.ReleaseMode},
		{gin.DebugMode, gin.DebugMode},
		{gin.TestMode, gin.TestMode},
	}
	for _, c := range cases {
		if got := resolveGinMode(c.in); got != c.want {
			t.Errorf("resolveGinMode(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}
}
