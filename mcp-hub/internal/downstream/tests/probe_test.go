package tests

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/hydra/mcp-hub/internal/downstream"
)

func TestProbeRemoteSuccess(t *testing.T) {
	ts := fakeMCPServer(t, 3)

	spec := downstream.Spec{Key: "fake", Name: "fake", Transport: downstream.TransportStreamableHTTP, URL: ts.URL}
	res := downstream.NewProber(spec).Probe(context.Background())

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

	spec := downstream.Spec{
		Key: "fake", Name: "fake",
		Transport: downstream.TransportStreamableHTTP, URL: ts.URL,
		LegacyOnly: true,
	}
	res := downstream.NewProber(spec).Probe(context.Background())

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

	cases := []struct {
		name    string
		url     string
		timeout time.Duration
		want    downstream.ErrorKind
	}{
		{"HTML 页面不是 MCP", htmlSrv.URL, 5 * time.Second, downstream.KindNotMCP},
		{"401 需要认证", statusServer(t, http.StatusUnauthorized).URL, 5 * time.Second, downstream.KindAuthRequired},
		{"403 禁止访问", statusServer(t, http.StatusForbidden).URL, 5 * time.Second, downstream.KindAuthRequired},
		{"404 路径不存在", statusServer(t, http.StatusNotFound).URL, 5 * time.Second, downstream.KindNotFound},
		{"500 下游异常", statusServer(t, http.StatusInternalServerError).URL, 5 * time.Second, downstream.KindUpstreamError},

		// 429 有两种含义，只有响应体能区分。判错会让一把没钱的密钥被无限退避重试。
		{"429 限流", gatedServer(t, `{"error":{"message":"rate limit reached"}}`).URL,
			5 * time.Second, downstream.KindRateLimited},
		{"429 额度耗尽", gatedServer(t, `{"error":{"message":"You exceeded your current quota","code":"insufficient_quota"}}`).URL,
			5 * time.Second, downstream.KindQuotaExhausted},
		{"402 需付费", statusServer(t, http.StatusPaymentRequired).URL, 5 * time.Second, downstream.KindQuotaExhausted},

		{"响应超时", slowSrv.URL, 400 * time.Millisecond, downstream.KindTimeout},
		{"连接被拒绝", "http://127.0.0.1:1/mcp", 2 * time.Second, downstream.KindConnRefused},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := downstream.Spec{
				Key: "fake", Name: "fake",
				Transport: downstream.TransportStreamableHTTP, URL: tc.url,
				Timeout: tc.timeout,
			}
			res := downstream.NewProber(spec).Probe(context.Background())

			if res.OK {
				t.Fatalf("期望失败，实际成功")
			}
			if res.Kind != tc.want {
				t.Errorf("Kind = %s, want %s (stage=%s detail=%s)", res.Kind, tc.want, res.Stage, res.Detail)
			}
		})
	}
}

func TestProbeStdioSuccess(t *testing.T) {
	bin := echoServerBinary(t)

	spec := downstream.Spec{Key: "echo", Name: "echo", Transport: downstream.TransportStdio, Command: bin}
	res := downstream.NewProber(spec).Probe(context.Background())

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
	spec := downstream.Spec{
		Key: "gone", Name: "gone",
		Transport: downstream.TransportStdio,
		Command:   "/nonexistent/definitely-not-a-binary",
		Timeout:   5 * time.Second,
	}
	res := downstream.NewProber(spec).Probe(context.Background())

	if res.OK {
		t.Fatal("不存在的命令不应探活成功")
	}
	if res.Kind != downstream.KindSpawnFailed {
		t.Errorf("Kind = %s, want %s (detail=%s)", res.Kind, downstream.KindSpawnFailed, res.Detail)
	}
}

func TestProbeStdioExitEarly(t *testing.T) {
	exitBin := "/usr/bin/false"
	if _, err := os.Stat(exitBin); err != nil {
		t.Skipf("%s 不存在，跳过", exitBin)
	}

	spec := downstream.Spec{
		Key: "dying", Name: "dying",
		Transport: downstream.TransportStdio,
		Command:   exitBin,
		Timeout:   5 * time.Second,
	}
	res := downstream.NewProber(spec).Probe(context.Background())

	if res.OK {
		t.Fatal("立即退出的进程不应探活成功")
	}
	if res.Kind != downstream.KindExitEarly {
		t.Errorf("Kind = %s, want %s (detail=%s)", res.Kind, downstream.KindExitEarly, res.Detail)
	}
}
