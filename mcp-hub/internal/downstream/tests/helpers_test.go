package tests

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/hydra/mcp-hub/internal/downstream"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// fakeClock 让退避与冷却在测试里可被推进。
// 不注入时钟的话，验证"1s→2s→4s 递增"得真睡 7 秒，而慢用例会被跳过——
// 跳过的恰好是最容易写错的退避逻辑。
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// testCreds 造 n 把互不相同的密钥。
func testCreds(n int) []downstream.CredentialInput {
	out := make([]downstream.CredentialInput, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, downstream.CredentialInput{
			ID:      fmt.Sprintf("key-%d", i+1),
			Label:   fmt.Sprintf("密钥 %d", i+1),
			Headers: map[string]string{"Authorization": fmt.Sprintf("Bearer secret-%d", i+1)},
			Enabled: true,
			State:   downstream.KeyActive,
		})
	}
	return out
}

// newTestRing 构造一个关了抖动、时间可控的密钥池。
func newTestRing(t *testing.T, clock *fakeClock, creds []downstream.CredentialInput,
	tune ...func(*downstream.KeyRingOptions)) *downstream.KeyRing {
	t.Helper()

	opts := downstream.KeyRingOptions{
		ServerID:    "srv-1",
		ServerKey:   "demo",
		JitterRatio: -1, // 显式关闭抖动：测试要的是确定性
		Now:         clock.Now,
	}
	for _, fn := range tune {
		fn(&opts)
	}
	return downstream.NewKeyRing(opts, creds)
}

// acquireOK 借一把密钥，失败即测试失败。
func acquireOK(t *testing.T, r *downstream.KeyRing) *downstream.KeyLease {
	t.Helper()

	lease, err := r.Acquire(nil)
	if err != nil {
		t.Fatalf("Acquire 失败: %v", err)
	}
	return lease
}

// fakeMCPServer 起一个真实的 MCP server（不是 mock 协议），覆盖完整协议路径。
func fakeMCPServer(t *testing.T, tools int) *httptest.Server {
	t.Helper()

	s := server.NewMCPServer("fake-mcp", "9.9.9", server.WithToolCapabilities(false))
	handler := func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText("ok"), nil
	}
	for i := range tools {
		s.AddTool(mcp.NewTool("tool_"+fmt.Sprint(i), mcp.WithDescription("test tool")), handler)
	}

	ts := httptest.NewServer(server.NewStreamableHTTPServer(s))
	t.Cleanup(ts.Close)
	return ts
}

// statusServer 起一个固定回某个状态码的 HTTP 端点。
func statusServer(t *testing.T, code int) *httptest.Server {
	t.Helper()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(code)
	}))
	t.Cleanup(ts.Close)
	return ts
}

// gatedServer 起一个回 429 的端点，body 里带指定文案——限流与额度耗尽只能靠它区分。
func gatedServer(t *testing.T, body string) *httptest.Server {
	t.Helper()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(ts.Close)
	return ts
}

var (
	echoOnce sync.Once
	echoPath string
	echoErr  error
)

// echoServerBinary 现场编译测试用的 stdio MCP server（不提交二进制），只构建一次。
func echoServerBinary(t *testing.T) string {
	t.Helper()

	echoOnce.Do(func() {
		goBin := goBinary()
		if goBin == "" {
			echoErr = errors.New("未找到 go 可执行文件")
			return
		}
		dir, err := os.MkdirTemp("", "mcp-hub-echoserver")
		if err != nil {
			echoErr = err
			return
		}
		out := filepath.Join(dir, "echoserver")

		cmd := exec.Command(goBin, "build", "-o", out, "../testdata/echoserver")
		if b, err := cmd.CombinedOutput(); err != nil {
			echoErr = fmt.Errorf("构建失败: %v: %s", err, b)
			return
		}
		echoPath = out
	})

	if echoErr != nil {
		t.Skipf("跳过 stdio 集成测试: %v", echoErr)
	}
	return echoPath
}

// goBinary 定位 go 可执行文件。测试进程的 PATH 可能不含它。
func goBinary() string {
	if root := runtime.GOROOT(); root != "" {
		p := filepath.Join(root, "bin", "go")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	if p, err := exec.LookPath("go"); err == nil {
		return p
	}
	return ""
}
