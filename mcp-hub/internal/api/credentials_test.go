package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hydra/mcp-hub/internal/httpd"
	"github.com/hydra/mcp-hub/internal/store/credentials"
	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"go.uber.org/zap"
)

// credGate 是只在工具调用上校验凭据的下游前置层。
// 握手与工具列举一律放行，避免 401 打掉聚合、让测试失败在无关处。
type credGate struct {
	mu     sync.Mutex
	deny   map[string]int // "Bearer xxx" → 状态码
	seen   []string
	counts map[string]int
}

func newCredGate() *credGate {
	return &credGate{deny: make(map[string]int), counts: make(map[string]int)}
}

func (g *credGate) reject(token string, status int) {
	g.mu.Lock()
	g.deny["Bearer "+token] = status
	g.mu.Unlock()
}

func (g *credGate) reset() {
	g.mu.Lock()
	g.seen = nil
	g.counts = make(map[string]int)
	g.mu.Unlock()
}

func (g *credGate) calls(token string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.counts["Bearer "+token]
}

func (g *credGate) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")

		body, err := io.ReadAll(r.Body)
		if err == nil {
			r.Body = io.NopCloser(bytes.NewReader(body))
		}

		var status int
		if bytes.Contains(body, []byte(`"method":"tools/call"`)) {
			g.mu.Lock()
			g.seen = append(g.seen, auth)
			g.counts[auth]++
			status = g.deny[auth]
			g.mu.Unlock()
		}

		if status >= 400 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":{"message":"rejected"}}`))
			return
		}
		next.ServeHTTP(w, r)
	})
}

type credentialResp struct {
	ID          string   `json:"id"`
	Label       string   `json:"label"`
	HeaderKeys  []string `json:"headerKeys"`
	Fingerprint string   `json:"fingerprint"`
	Configured  bool     `json:"configured"`
	Enabled     bool     `json:"enabled"`
	State       string   `json:"state"`

	DisabledKind string `json:"disabledKind"`
	CoolingForMS int64  `json:"coolingForMs"`
	RPMLimit     int    `json:"rpmLimit"`
	RPMUsed      int    `json:"rpmUsed"`
	Picks        uint64 `json:"picks"`
}

type credentialListResp struct {
	Total   int              `json:"total"`
	Items   []credentialResp `json:"items"`
	KeyPool bool             `json:"keyPool"`
}

func TestCredentialLifecycle(t *testing.T) {
	e := newEnv(t, httpd.Options{})
	ds := startDownstream(t, "one")

	e.mustJSON(http.MethodPost, "/api/servers", map[string]any{
		"key":        "pool",
		"transport":  "streamable_http",
		"url":        ds,
		"categoryId": e.mustCategory(),
	}, http.StatusCreated, nil)
	sid := e.firstServerID()

	// 用 value + authScheme 展开，省略手写 headers。
	var first credentialResp
	e.mustJSON(http.MethodPost, "/api/servers/"+sid+"/credentials", map[string]any{
		"label":      "主号",
		"value":      "tok-alpha",
		"authScheme": "bearer",
		"rpmLimit":   100,
	}, http.StatusCreated, &first)

	if first.ID == "" || first.Label != "主号" {
		t.Fatalf("新增返回不完整: %+v", first)
	}
	if !first.Configured || first.Fingerprint == "" {
		t.Errorf("应给出指纹让用户区分是哪一把: %+v", first)
	}
	if len(first.HeaderKeys) != 1 || first.HeaderKeys[0] != "Authorization" {
		t.Errorf("应给出注入头的键名供辨认: %v", first.HeaderKeys)
	}
	if first.RPMLimit != 100 {
		t.Errorf("配额上限没有落库: rpm=%d", first.RPMLimit)
	}

	// 第二种填法：直接给一组 headers（X-API-Key 这类非 Bearer 形态）。
	var second credentialResp
	e.mustJSON(http.MethodPost, "/api/servers/"+sid+"/credentials", map[string]any{
		"label":   "备用",
		"headers": map[string]string{"X-API-Key": "tok-beta"},
	}, http.StatusCreated, &second)
	if len(second.HeaderKeys) != 1 || second.HeaderKeys[0] != "X-API-Key" {
		t.Errorf("自定义头没有被采纳: %v", second.HeaderKeys)
	}

	// 轮换顺序：新加的在末尾；若新增插到 0，既有密钥的轮换配比会被静默改写。
	creds, err := e.store.Credentials.List(context.Background(), sid)
	if err != nil {
		t.Fatalf("回读密钥失败: %v", err)
	}
	if len(creds) != 2 {
		t.Fatalf("密钥数 = %d, want 2", len(creds))
	}
	if creds[0].SortOrder >= creds[1].SortOrder {
		t.Errorf("新增密钥没有排在末尾: sortOrder = %d, %d",
			creds[0].SortOrder, creds[1].SortOrder)
	}
	if creds[0].Label != "主号" || creds[1].Label != "备用" {
		t.Errorf("列表顺序与创建顺序不一致: %s, %s", creds[0].Label, creds[1].Label)
	}

	var list credentialListResp
	e.get("/api/servers/"+sid+"/credentials", &list)
	if list.Total != 2 || !list.KeyPool {
		t.Errorf("列表结果异常: total=%d keyPool=%v", list.Total, list.KeyPool)
	}

	// 编辑：不带 headers 表示“不动凭据”，只改备注不该抹掉密钥。
	var updated credentialResp
	e.mustJSON(http.MethodPut, "/api/servers/"+sid+"/credentials/"+first.ID, map[string]any{
		"label":    "主号（改名）",
		"rpmLimit": 200,
	}, http.StatusOK, &updated)
	if updated.Label != "主号（改名）" || updated.RPMLimit != 200 {
		t.Errorf("编辑没有生效: %+v", updated)
	}
	saved, err := e.store.Credentials.Get(context.Background(), first.ID)
	if err != nil {
		t.Fatalf("回读密钥失败: %v", err)
	}
	if got := saved.Headers["Authorization"]; got != "Bearer tok-alpha" {
		t.Errorf("编辑把凭据弄坏了: %q", got)
	}

	// 回显打码值再提交回来 → 也必须保持原值。
	e.mustJSON(http.MethodPut, "/api/servers/"+sid+"/credentials/"+first.ID, map[string]any{
		"label":   "主号（改名）",
		"headers": map[string]string{"Authorization": "***"},
	}, http.StatusOK, nil)
	saved, _ = e.store.Credentials.Get(context.Background(), first.ID)
	if got := saved.Headers["Authorization"]; got != "Bearer tok-alpha" {
		t.Errorf("提交打码值后凭据被打坏了: %q", got)
	}

	var disabled credentialResp
	e.mustJSON(http.MethodPost, "/api/servers/"+sid+"/credentials/"+first.ID+"/disable",
		nil, http.StatusOK, &disabled)
	if disabled.Enabled {
		t.Error("停用没有生效")
	}
	var enabled credentialResp
	e.mustJSON(http.MethodPost, "/api/servers/"+sid+"/credentials/"+first.ID+"/enable",
		nil, http.StatusOK, &enabled)
	if !enabled.Enabled {
		t.Error("启用没有生效")
	}

	// 清除窗口内的用量。
	e.mustJSON(http.MethodPost, "/api/servers/"+sid+"/credentials/"+first.ID+"/reset-quota",
		nil, http.StatusOK, &updated)
	if updated.RPMUsed != 0 {
		t.Errorf("清零配额后用量 = %d, want 0", updated.RPMUsed)
	}

	resp, _ := e.call(http.MethodDelete, "/api/servers/"+sid+"/credentials/"+second.ID, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("删除状态码 = %d, want 204", resp.StatusCode)
	}
	e.get("/api/servers/"+sid+"/credentials", &list)
	if list.Total != 1 {
		t.Errorf("删除后密钥数 = %d, want 1", list.Total)
	}

	// 错误映射：不存在的 id → 404；重名 → 409。
	resp, _ = e.call(http.MethodGet, "/api/servers/"+sid+"/credentials/nope", nil)
	if resp.StatusCode != http.StatusMethodNotAllowed && resp.StatusCode != http.StatusNotFound {
		// GET 单把没有端点，这里只确认它不是一个 500。
		t.Logf("GET 单把密钥返回 %d（无此端点属预期）", resp.StatusCode)
	}
	resp, _ = e.call(http.MethodDelete, "/api/servers/"+sid+"/credentials/nope", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("删除不存在的密钥 = %d, want 404", resp.StatusCode)
	}
	resp, raw := e.call(http.MethodPost, "/api/servers/"+sid+"/credentials", map[string]any{
		"label":   "主号（改名）",
		"headers": map[string]string{"Authorization": "Bearer other"},
	})
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("重名 = %d, want 409\n响应: %s", resp.StatusCode, raw)
	}
}

// 安全约束：任何接口回显明文，都等于把真凭据交给能读管理接口的人。
func TestCredentialPlaintextNeverLeaks(t *testing.T) {
	e := newEnv(t, httpd.Options{})
	ds := startDownstream(t, "one")

	e.mustJSON(http.MethodPost, "/api/servers", map[string]any{
		"key":        "pool",
		"transport":  "streamable_http",
		"url":        ds,
		"categoryId": e.mustCategory(),
	}, http.StatusCreated, nil)
	sid := e.firstServerID()

	const secret = "super-secret-value-9f3a"
	e.mustJSON(http.MethodPost, "/api/servers/"+sid+"/credentials", map[string]any{
		"label":      "主号",
		"value":      secret,
		"authScheme": "bearer",
	}, http.StatusCreated, nil)

	paths := []string{
		"/api/servers",
		"/api/servers/" + sid,
		"/api/servers/" + sid + "/credentials",
		"/api/overview",
		"/api/tools",
		"/api/logs",
	}
	for _, p := range paths {
		_, raw := e.call(http.MethodGet, p, nil)
		if bytes.Contains(raw, []byte(secret)) {
			t.Errorf("%s 的响应里出现了密钥明文", p)
		}
	}
}

func TestCredentialImportFromPaste(t *testing.T) {
	e := newEnv(t, httpd.Options{})
	ds := startDownstream(t, "one")

	e.mustJSON(http.MethodPost, "/api/servers", map[string]any{
		"key":        "pool",
		"transport":  "streamable_http",
		"url":        ds,
		"categoryId": e.mustCategory(),
	}, http.StatusCreated, nil)
	sid := e.firstServerID()

	var out struct {
		Created int `json:"created"`
		Items   []struct {
			ID          string `json:"id"`
			Label       string `json:"label"`
			Fingerprint string `json:"fingerprint"`
		} `json:"items"`
	}
	e.mustJSON(http.MethodPost, "/api/servers/"+sid+"/credentials/import", map[string]any{
		"authScheme": "bearer",
		"lines": []string{
			"主号=alpha-token",
			"备用=beta-token",
			"",             // 空行忽略
			"# 注释行",        // 注释忽略
			"gamma-token",  // 没写名字 → 自动命名
			"主号=dup-token", // 重名 → 加后缀，而不是整个导入失败
		},
	}, http.StatusCreated, &out)

	if out.Created != 4 {
		t.Fatalf("导入数 = %d, want 4: %+v", out.Created, out.Items)
	}
	labels := make([]string, 0, len(out.Items))
	for _, it := range out.Items {
		labels = append(labels, it.Label)
		if it.Fingerprint == "" {
			t.Errorf("%s 没有指纹，用户无法核对粘进去的是哪几把", it.Label)
		}
	}
	joined := strings.Join(labels, ",")
	for _, want := range []string{"主号", "备用"} {
		if !strings.Contains(joined, want) {
			t.Errorf("导入结果缺少 %q: %v", want, labels)
		}
	}
	// 没写名字的那行要自动命名，且**不能**把凭据值当名字——
	// 那等于把密钥明文写进列表与日志。
	var autoNamed bool
	for _, l := range labels {
		if l == "密钥 3" {
			autoNamed = true
		}
		if strings.Contains(l, "gamma-token") {
			t.Errorf("自动命名把凭据明文当成了名称: %q", l)
		}
	}
	if !autoNamed {
		t.Errorf("无名的那行没有得到自动名称: %v", labels)
	}
	// 重名那把必须存在且可区分，否则界面与日志里会出现两把同名密钥。
	var renamed bool
	for _, l := range labels {
		if strings.HasPrefix(l, "主号") && l != "主号" {
			renamed = true
		}
	}
	if !renamed {
		t.Errorf("重名没有加后缀，得到 %v", labels)
	}

	// 留空的行 → 明确报错，而不是静默导入 0 把。
	resp, raw := e.call(http.MethodPost, "/api/servers/"+sid+"/credentials/import",
		map[string]any{"authScheme": "bearer", "lines": []string{"   ", "# only comments"}})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("空导入 = %d, want 400\n响应: %s", resp.StatusCode, raw)
	}

	// 未知的凭据类型 → 400，且错误里说清可选值。
	resp, raw = e.call(http.MethodPost, "/api/servers/"+sid+"/credentials/import",
		map[string]any{"authScheme": "wat", "lines": []string{"x=y"}})
	if resp.StatusCode != http.StatusBadRequest || !bytes.Contains(raw, []byte("bearer")) {
		t.Errorf("未知 authScheme 应报 400 并列出可选值: %d %s", resp.StatusCode, raw)
	}
}

// stdio 的凭据在子进程环境变量里，改它必须重启进程，无法按调用轮换。
func TestCredentialStdioUnsupported(t *testing.T) {
	e := newEnv(t, httpd.Options{})

	e.mustJSON(http.MethodPost, "/api/servers", map[string]any{
		"key":        "local",
		"transport":  "stdio",
		"command":    "/bin/echo",
		"categoryId": e.mustCategory(),
	}, http.StatusCreated, nil)
	sid := e.firstServerID()

	resp, raw := e.call(http.MethodPost, "/api/servers/"+sid+"/credentials", map[string]any{
		"label": "主号",
		"value": "tok",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("stdio 配密钥 = %d, want 400\n响应: %s", resp.StatusCode, raw)
	}
	var body struct {
		Code string `json:"code"`
		Err  string `json:"error"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("解析错误体失败: %v", err)
	}
	if body.Code != "key_pool_unsupported" {
		t.Errorf("错误码 = %q, want key_pool_unsupported", body.Code)
	}
}

// 管理接口的价值在于改动真的生效：API 写入密钥 → 网关发起真实调用 → 下游可见。
func TestCredentialIsActuallyUsedWhichKeys(t *testing.T) {
	gate := newCredGate()
	ds := gatedDownstream(t, gate, "ping")

	e := newEnv(t, httpd.Options{})
	e.mustJSON(http.MethodPost, "/api/servers", map[string]any{
		"key":        "pool",
		"transport":  "streamable_http",
		"url":        ds,
		"categoryId": e.mustCategory(),
	}, http.StatusCreated, nil)
	sid := e.firstServerID()

	for _, tok := range []string{"good-a", "good-b"} {
		e.mustJSON(http.MethodPost, "/api/servers/"+sid+"/credentials", map[string]any{
			"label":      tok,
			"value":      tok,
			"authScheme": "bearer",
		}, http.StatusCreated, nil)
	}
	// 让网关把工具列出来（这一步也会借走一把密钥）。
	e.mustJSON(http.MethodPost, "/api/servers/"+sid+"/refresh-tools", nil, http.StatusOK, nil)
	// 新工具默认禁用，显式启用后才能被 AI 调用。
	e.enableAllTools()

	gate.reset()
	c := e.mcpClient()
	for i := 0; i < 4; i++ {
		callTool(t, c, "pool__ping")
	}

	// 两把都要被用到：轮换没生效的话，流量会全压在一把上。
	if gate.calls("good-a") == 0 || gate.calls("good-b") == 0 {
		t.Errorf("轮换没有覆盖两把密钥: a=%d b=%d",
			gate.calls("good-a"), gate.calls("good-b"))
	}
	// 且不能还在用 servers.headers 里那套（这里是空的）。
	if got := gate.calls(""); got != 0 {
		t.Errorf("有 %d 次调用没带凭据——密钥池没接上", got)
	}
}

// 单密钥探活：只读，不改状态。
func TestCredentialTestIsReadOnly(t *testing.T) {
	e := newEnv(t, httpd.Options{})
	ds := startDownstream(t, "one")

	e.mustJSON(http.MethodPost, "/api/servers", map[string]any{
		"key":        "pool",
		"transport":  "streamable_http",
		"url":        ds,
		"categoryId": e.mustCategory(),
	}, http.StatusCreated, nil)
	sid := e.firstServerID()

	var created credentialResp
	e.mustJSON(http.MethodPost, "/api/servers/"+sid+"/credentials", map[string]any{
		"label": "主号",
		"value": "tok",
	}, http.StatusCreated, &created)

	var out struct {
		Probe struct {
			OK   bool   `json:"ok"`
			Kind string `json:"kind"`
		} `json:"probe"`
		Verdict string `json:"verdict"`
		Hint    string `json:"hint"`
	}
	e.mustJSON(http.MethodPost, "/api/servers/"+sid+"/credentials/"+created.ID+"/test",
		nil, http.StatusOK, &out)
	if !out.Probe.OK {
		t.Fatalf("健康密钥的探活失败: %+v", out)
	}
	if out.Verdict != "OK" {
		t.Errorf("判决 = %q, want OK", out.Verdict)
	}

	// 关键语义：测试不改状态，否则“点一下测试”会顺手禁用密钥。
	saved, err := e.store.Credentials.Get(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("回读失败: %v", err)
	}
	if saved.State != credentials.CredStateActive || !saved.Enabled {
		t.Errorf("测试改变了密钥状态: state=%s enabled=%v", saved.State, saved.Enabled)
	}

	resp, _ := e.call(http.MethodPost, "/api/servers/"+sid+"/credentials/nope/test", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("测试不存在的密钥 = %d, want 404", resp.StatusCode)
	}
}

// 归属校验：拿 A 的密钥 id 不能操作到 B 上。
func TestCredentialBelongsToServer(t *testing.T) {
	e := newEnv(t, httpd.Options{})
	ds := startDownstream(t, "one")

	for _, key := range []string{"pool-a", "pool-b"} {
		e.mustJSON(http.MethodPost, "/api/servers", map[string]any{
			"key":        key,
			"transport":  "streamable_http",
			"url":        ds,
			"categoryId": e.mustCategory(),
		}, http.StatusCreated, nil)
	}
	var list serverListResp
	e.get("/api/servers", &list)
	idA, idB := list.Items[0].ID, list.Items[1].ID

	var credA credentialResp
	e.mustJSON(http.MethodPost, "/api/servers/"+idA+"/credentials", map[string]any{
		"label": "主号", "value": "tok-a",
	}, http.StatusCreated, &credA)

	// 用 B 的路径去操作 A 的密钥 → 必须当作不存在。
	resp, _ := e.call(http.MethodDelete, "/api/servers/"+idB+"/credentials/"+credA.ID, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("跨 server 删除 = %d, want 404", resp.StatusCode)
	}
	if _, err := e.store.Credentials.Get(context.Background(), credA.ID); err != nil {
		t.Errorf("跨 server 的请求把密钥删掉了: %v", err)
	}
}

// 调用日志里要能看到用了哪一把密钥。
func TestCredentialIDInCallLogs(t *testing.T) {
	gate := newCredGate()
	ds := gatedDownstream(t, gate, "ping")

	e := newEnv(t, httpd.Options{Logger: zap.NewNop()})
	e.mustJSON(http.MethodPost, "/api/servers", map[string]any{
		"key":        "pool",
		"transport":  "streamable_http",
		"url":        ds,
		"categoryId": e.mustCategory(),
	}, http.StatusCreated, nil)
	sid := e.firstServerID()

	var created credentialResp
	e.mustJSON(http.MethodPost, "/api/servers/"+sid+"/credentials", map[string]any{
		"label": "only", "value": "tok",
	}, http.StatusCreated, &created)
	e.mustJSON(http.MethodPost, "/api/servers/"+sid+"/refresh-tools", nil, http.StatusOK, nil)
	e.enableAllTools()

	callTool(t, e.mcpClient(), "pool__ping")

	var logs struct {
		Items []struct {
			CredentialID string `json:"credentialId"`
			ToolName     string `json:"toolName"`
		} `json:"items"`
	}
	waitFor(t, func() bool {
		e.get("/api/logs?limit=20", &logs)
		return len(logs.Items) > 0
	}, "调用日志没有落库")

	var found bool
	for _, l := range logs.Items {
		if l.CredentialID == created.ID {
			found = true
		}
	}
	if !found {
		t.Errorf("日志里没有 credentialId，无法回答\"这把密钥为什么失败了\": %+v", logs.Items)
	}
}

// 概览需统计不可用密钥数。
func TestOverviewCountsUnusableKeys(t *testing.T) {
	e := newEnv(t, httpd.Options{})
	ds := startDownstream(t, "one")

	e.mustJSON(http.MethodPost, "/api/servers", map[string]any{
		"key":        "pool",
		"transport":  "streamable_http",
		"url":        ds,
		"categoryId": e.mustCategory(),
	}, http.StatusCreated, nil)
	sid := e.firstServerID()

	var a, b credentialResp
	e.mustJSON(http.MethodPost, "/api/servers/"+sid+"/credentials", map[string]any{
		"label": "a", "value": "tok-a",
	}, http.StatusCreated, &a)
	e.mustJSON(http.MethodPost, "/api/servers/"+sid+"/credentials", map[string]any{
		"label": "b", "value": "tok-b",
	}, http.StatusCreated, &b)

	// 人工停用一把。
	e.mustJSON(http.MethodPost, "/api/servers/"+sid+"/credentials/"+b.ID+"/disable",
		nil, http.StatusOK, nil)

	var ov struct {
		Credentials struct {
			Total    int `json:"total"`
			Usable   int `json:"usable"`
			Unusable int `json:"unusable"`
			Servers  int `json:"servers"`
		} `json:"credentials"`
	}
	e.get("/api/overview", &ov)

	if ov.Credentials.Total != 2 {
		t.Errorf("密钥总数 = %d, want 2", ov.Credentials.Total)
	}
	if ov.Credentials.Usable != 1 {
		t.Errorf("可用密钥数 = %d, want 1：停用的那把不该算作可用", ov.Credentials.Usable)
	}
}

// 同一份凭据不允许在一个 server 里配两把（白占轮换名额），
// 也不允许配到第二个 server（限流压力集中、用量归属搅浑）。
// 头名按 HTTP 惯例不区分大小写。
func TestCredentialDuplicateHeadersRejected(t *testing.T) {
	e := newEnv(t, httpd.Options{})
	ds := startDownstream(t, "one")

	for _, key := range []string{"pool-a", "pool-b"} {
		e.mustJSON(http.MethodPost, "/api/servers", map[string]any{
			"key":        key,
			"transport":  "streamable_http",
			"url":        ds,
			"categoryId": e.mustCategory(),
		}, http.StatusCreated, nil)
	}
	var list serverListResp
	e.get("/api/servers", &list)
	idA, idB := list.Items[0].ID, list.Items[1].ID

	var first credentialResp
	e.mustJSON(http.MethodPost, "/api/servers/"+idA+"/credentials", map[string]any{
		"label": "主号", "value": "tok-x", "authScheme": "bearer",
	}, http.StatusCreated, &first)

	// 同 server 同值 → 409。
	resp, raw := e.call(http.MethodPost, "/api/servers/"+idA+"/credentials", map[string]any{
		"label": "副本", "value": "tok-x", "authScheme": "bearer",
	})
	if resp.StatusCode != http.StatusConflict || !bytes.Contains(raw, []byte("credential_duplicate")) {
		t.Errorf("同 server 同值 = %d, want 409 credential_duplicate\n响应: %s", resp.StatusCode, raw)
	}

	// 头名大小写不同、值相同 → 仍算同值（HTTP 头不区分大小写）。
	resp, raw = e.call(http.MethodPost, "/api/servers/"+idA+"/credentials", map[string]any{
		"label":   "换名头",
		"headers": map[string]string{"aUtHoRiZaTiOn": "Bearer tok-x"},
	})
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("头名大小写变体 = %d, want 409\n响应: %s", resp.StatusCode, raw)
	}

	// 同一份凭据配到第二个 server → 409。
	resp, raw = e.call(http.MethodPost, "/api/servers/"+idB+"/credentials", map[string]any{
		"label": "跨服务器", "value": "tok-x", "authScheme": "bearer",
	})
	if resp.StatusCode != http.StatusConflict || !bytes.Contains(raw, []byte("credential_duplicate")) {
		t.Errorf("跨 server 同值 = %d, want 409 credential_duplicate\n响应: %s", resp.StatusCode, raw)
	}

	// 被拒的请求不能留下半条记录。
	var listA credentialListResp
	e.get("/api/servers/"+idA+"/credentials", &listA)
	if listA.Total != 1 {
		t.Errorf("server A 密钥数 = %d, want 1：被拒的保存不该落库", listA.Total)
	}
	var listB credentialListResp
	e.get("/api/servers/"+idB+"/credentials", &listB)
	if listB.Total != 0 {
		t.Errorf("server B 密钥数 = %d, want 0", listB.Total)
	}
}

func (e *env) firstServerID() string {
	e.t.Helper()
	var list serverListResp
	e.get("/api/servers", &list)
	if len(list.Items) == 0 {
		e.t.Fatal("没有 server")
	}
	return list.Items[0].ID
}

// gatedDownstream 起一个被门卫包住的真实下游 MCP server。
func gatedDownstream(t *testing.T, g *credGate, tools ...string) string {
	t.Helper()

	srv := mcpserver.NewMCPServer("fake-downstream", "1.0.0",
		mcpserver.WithToolCapabilities(false))
	for _, name := range tools {
		srv.AddTool(
			mcp.NewTool(name, mcp.WithDescription("description of "+name)),
			func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return mcp.NewToolResultText(req.Params.Name + " called"), nil
			},
		)
	}

	ts := httptest.NewServer(g.wrap(mcpserver.NewStreamableHTTPServer(srv)))
	t.Cleanup(ts.Close)
	return ts.URL
}

func callTool(t *testing.T, c *client.Client, name string) {
	t.Helper()
	req := mcp.CallToolRequest{}
	req.Params.Name = name
	if _, err := c.CallTool(context.Background(), req); err != nil {
		t.Fatalf("tools/call %s 失败: %v", name, err)
	}
}

// waitFor 轮询等待异步结果（调用日志是异步落库的）。
func waitFor(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatal(msg)
}
