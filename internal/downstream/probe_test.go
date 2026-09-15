package downstream

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

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// fakeMCPServer 起一个真实的 MCP server（不是 mock 协议），
// 这样测试覆盖的是完整协议路径。
func fakeMCPServer(t *testing.T, tools int) *httptest.Server {
	t.Helper()

	s := server.NewMCPServer("fake-mcp", "9.9.9", server.WithToolCapabilities(false))
	handler := func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText("ok"), nil
	}
	for i := range tools {
		s.AddTool(mcp.NewTool(fmt.Sprintf("tool_%d", i), mcp.WithDescription("test tool")), handler)
	}

	ts := httptest.NewServer(server.NewStreamableHTTPServer(s))
	t.Cleanup(ts.Close)
	return ts
}

func TestProbeRemoteSuccess(t *testing.T) {
	ts := fakeMCPServer(t, 3)

	spec := Spec{Key: "fake", Name: "fake", Transport: TransportStreamableHTTP, URL: ts.URL}
	res := NewProber(spec).Probe(context.Background())

	if !res.OK {
		t.Fatalf("探活应当成功，得到 stage=%s kind=%s detail=%s", res.Stage, res.Kind, res.Detail)
	}
	if res.ServerName != "fake-mcp" {
		t.Errorf("ServerName = %q, want fake-mcp", res.ServerName)
	}
	if res.ServerVersion != "9.9.9" {
		t.Errorf("ServerVersion = %q, want 9.9.9", res.ServerVersion)
	}
	if res.ToolCount != 3 {
		t.Errorf("ToolCount = %d, want 3", res.ToolCount)
	}
	if res.ProtocolVersion == "" {
		t.Error("ProtocolVersion 应当被记录（用于缓存协商结果）")
	}
	if len(res.Tools) != 3 {
		t.Errorf("Tools = %d 项, want 3", len(res.Tools))
	}
	if res.Latency.Total < 0 {
		t.Error("Total 耗时异常")
	}
}

func TestProbeLegacyOnly(t *testing.T) {
	ts := fakeMCPServer(t, 1)

	spec := Spec{
		Key: "fake", Name: "fake",
		Transport: TransportStreamableHTTP, URL: ts.URL,
		LegacyOnly: true,
	}
	res := NewProber(spec).Probe(context.Background())

	if !res.OK {
		t.Fatalf("强制旧协议应当成功，得到 stage=%s kind=%s detail=%s", res.Stage, res.Kind, res.Detail)
	}
	if !res.Legacy {
		t.Errorf("LegacyOnly 下 Legacy 应为 true，协议版本 %q", res.ProtocolVersion)
	}
}

// TestProbeRemoteErrorKinds 覆盖每个可自动触发的错误分类。
func TestProbeRemoteErrorKinds(t *testing.T) {
	htmlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "<!doctype html><html><body>this is a website, not an MCP server</body></html>")
	}))
	t.Cleanup(htmlSrv.Close)

	slowSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(3 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(slowSrv.Close)

	statusSrv := func(code int) *httptest.Server {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(code)
		}))
		t.Cleanup(ts.Close)
		return ts
	}

	cases := []struct {
		name    string
		url     string
		timeout time.Duration
		want    ErrorKind
	}{
		{"HTML 页面不是 MCP", htmlSrv.URL, 5 * time.Second, KindNotMCP},
		{"401 需要认证", statusSrv(http.StatusUnauthorized).URL, 5 * time.Second, KindAuthRequired},
		{"403 禁止访问", statusSrv(http.StatusForbidden).URL, 5 * time.Second, KindAuthRequired},
		{"404 路径不存在", statusSrv(http.StatusNotFound).URL, 5 * time.Second, KindNotFound},
		{"500 下游异常", statusSrv(http.StatusInternalServerError).URL, 5 * time.Second, KindUpstreamError},
		{"响应超时", slowSrv.URL, 400 * time.Millisecond, KindTimeout},
		{"连接被拒绝", "http://127.0.0.1:1/mcp", 2 * time.Second, KindConnRefused},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := Spec{
				Key: "fake", Name: "fake",
				Transport: TransportStreamableHTTP, URL: tc.url,
				Timeout: tc.timeout,
			}
			res := NewProber(spec).Probe(context.Background())

			if res.OK {
				t.Fatalf("期望失败，实际成功")
			}
			if res.Kind != tc.want {
				t.Errorf("Kind = %s, want %s (stage=%s detail=%s)", res.Kind, tc.want, res.Stage, res.Detail)
			}
		})
	}
}

func TestProbeRejectsInvalidSpec(t *testing.T) {
	cases := []struct {
		name string
		spec Spec
	}{
		{"缺 key", Spec{Transport: TransportStreamableHTTP, URL: "http://x/mcp"}},
		{"key 含双下划线", Spec{Key: "a__b", Transport: TransportStreamableHTTP, URL: "http://x/mcp"}},
		{"远程缺 url", Spec{Key: "a", Transport: TransportStreamableHTTP}},
		{"stdio 缺 command", Spec{Key: "a", Transport: TransportStdio}},
		{"未知传输", Spec{Key: "a", Transport: "grpc"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := NewProber(tc.spec).Probe(context.Background())
			if res.OK {
				t.Fatal("非法配置不应探活成功")
			}
		})
	}
}

func TestProbeStdioSuccess(t *testing.T) {
	bin := echoServerBinary(t)

	spec := Spec{Key: "echo", Name: "echo", Transport: TransportStdio, Command: bin}
	res := NewProber(spec).Probe(context.Background())

	if !res.OK {
		t.Fatalf("stdio 探活应当成功，得到 stage=%s kind=%s detail=%s stderr=%s",
			res.Stage, res.Kind, res.Detail, res.Stderr)
	}
	if res.ServerName != "echoserver" {
		t.Errorf("ServerName = %q, want echoserver", res.ServerName)
	}
	if res.ToolCount != 2 {
		t.Errorf("ToolCount = %d, want 2", res.ToolCount)
	}
}

func TestProbeStdioSpawnFailed(t *testing.T) {
	spec := Spec{
		Key: "gone", Name: "gone",
		Transport: TransportStdio,
		Command:   "/nonexistent/definitely-not-a-binary",
		Timeout:   5 * time.Second,
	}
	res := NewProber(spec).Probe(context.Background())

	if res.OK {
		t.Fatal("不存在的命令不应探活成功")
	}
	if res.Kind != KindSpawnFailed {
		t.Errorf("Kind = %s, want %s (detail=%s)", res.Kind, KindSpawnFailed, res.Detail)
	}
}

func TestProbeStdioExitEarly(t *testing.T) {
	exitBin := "/usr/bin/false"
	if _, err := os.Stat(exitBin); err != nil {
		t.Skipf("%s 不存在，跳过", exitBin)
	}

	spec := Spec{
		Key: "dying", Name: "dying",
		Transport: TransportStdio,
		Command:   exitBin,
		Timeout:   5 * time.Second,
	}
	res := NewProber(spec).Probe(context.Background())

	if res.OK {
		t.Fatal("立即退出的进程不应探活成功")
	}
	if res.Kind != KindExitEarly {
		t.Errorf("Kind = %s, want %s (detail=%s)", res.Kind, KindExitEarly, res.Detail)
	}
}

// echoServerBinary 构建测试用的 stdio MCP server，只构建一次。
var (
	echoOnce sync.Once
	echoPath string
	echoErr  error
)

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

		cmd := exec.Command(goBin, "build", "-o", out, "./testdata/echoserver")
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
