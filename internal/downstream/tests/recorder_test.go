package tests

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hydra/mcp-hub/internal/downstream"
)

// getWith 用给定的 RoundTripper 发一次 GET，返回响应体与观测结果。
func getWith(t *testing.T, rec *downstream.Recorder, url string) (string, downstream.Observation) {
	t.Helper()

	resp, err := (&http.Client{Transport: rec}).Get(url)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读响应体失败: %v", err)
	}
	return string(body), rec.Last()
}

// Retry-After 的两种格式都要认：RFC 7231 允许秒数与 HTTP-date，实际供应商
// 两种都在用。解析不出来时留 0，而不是猜一个值——猜会把"下游说 30s 后好"
// 悄悄改成别的数字。
func TestRecorderParsesRetryAfter(t *testing.T) {
	now := time.Now()

	cases := []struct {
		name    string
		header  string
		wantMin time.Duration
		wantMax time.Duration
	}{
		{"秒数", "30", 30 * time.Second, 30 * time.Second},
		{"HTTP-date", now.Add(90 * time.Second).UTC().Format(http.TimeFormat), 85 * time.Second, 90 * time.Second},
		{"无法解析", "soon", 0, 0},
		{"负数", "-5", 0, 0},
		{"空", "", 0, 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tc.header != "" {
					w.Header().Set("Retry-After", tc.header)
				}
				w.WriteHeader(http.StatusTooManyRequests)
			}))
			defer srv.Close()

			_, obs := getWith(t, downstream.NewRecorder(), srv.URL)
			if obs.StatusCode != http.StatusTooManyRequests {
				t.Fatalf("StatusCode = %d, want 429", obs.StatusCode)
			}
			if obs.RetryAfter < tc.wantMin || obs.RetryAfter > tc.wantMax {
				t.Errorf("RetryAfter = %s, want [%s, %s]", obs.RetryAfter, tc.wantMin, tc.wantMax)
			}
		})
	}
}

// 错误响应体只看开头若干字节（用于区分限流与额度耗尽），但必须对调用方保持
// 完整：偷看不能把响应体吃掉。
func TestRecorderPeekKeepsBodyReadable(t *testing.T) {
	const marker = `{"error":{"message":"insufficient_quota"}}`
	body := marker + strings.Repeat("x", 1000)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	got, obs := getWith(t, downstream.NewRecorder(), srv.URL)

	if got != body {
		t.Errorf("响应体被改动：读到 %d 字节, want %d", len(got), len(body))
	}
	if !strings.Contains(obs.BodySnippet, "insufficient_quota") {
		t.Errorf("响应体片段里没有额度标记: %q", obs.BodySnippet)
	}
	if len(obs.BodySnippet) > 512 {
		t.Errorf("响应体片段 %d 字节，超出上限：这个观察点不该变成内存放大的入口", len(obs.BodySnippet))
	}
}

// 事件流不偷看：正常响应可能是 SSE 长连接，缓冲它会破坏流式语义。
func TestRecorderSkipsEventStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte("data: too many\n\n"))
	}))
	defer srv.Close()

	got, obs := getWith(t, downstream.NewRecorder(), srv.URL)

	if obs.BodySnippet != "" {
		t.Errorf("事件流被偷看了: %q", obs.BodySnippet)
	}
	if got != "data: too many\n\n" {
		t.Errorf("事件流内容被改动: %q", got)
	}
}
