package tests

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hydra/mcp-hub/internal/downstream"
	"github.com/hydra/mcp-hub/internal/store/credentials"
	"github.com/hydra/mcp-hub/internal/store/logs"
)

// keyedGate 是按凭据给不同答复的下游门卫。
//
// 只统计**工具调用**请求：一次调用在下游侧会产生不止一条 HTTP 往返
// （事件流通道会另开一条），把那些也算进去会让"这把密钥被打了几次"
// 变成 2 的倍数。握手与工具列举同样不计——它们与密钥轮换无关。
type keyedGate struct {
	mu         sync.Mutex
	policy     map[string]int // "Bearer xxx" → 状态码
	retryAfter string
	seen       []string
	calls      map[string]int
}

func newKeyedGate(retryAfter string) *keyedGate {
	return &keyedGate{
		policy:     make(map[string]int),
		calls:      make(map[string]int),
		retryAfter: retryAfter,
	}
}

// deny 让某个凭据在工具调用上被拒。
func (g *keyedGate) deny(token string, status int) {
	g.mu.Lock()
	g.policy["Bearer "+token] = status
	g.mu.Unlock()
}

// Credentials 返回下游在工具调用上依次收到的 Authorization 头。
func (g *keyedGate) Credentials() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.seen...)
}

// Calls 返回某个凭据在工具调用上被用到的次数。
func (g *keyedGate) Calls(token string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.calls["Bearer "+token]
}

func (g *keyedGate) reset() {
	g.mu.Lock()
	g.seen = nil
	g.calls = make(map[string]int)
	g.mu.Unlock()
}

func (g *keyedGate) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get("Authorization")

		body, err := io.ReadAll(r.Body)
		if err == nil {
			r.Body = io.NopCloser(bytes.NewReader(body))
		}

		var status int
		if isToolCall(body) {
			g.mu.Lock()
			g.seen = append(g.seen, got)
			g.calls[got]++
			status = g.policy[got]
			g.mu.Unlock()
		}

		if status >= 400 {
			if g.retryAfter != "" {
				w.Header().Set("Retry-After", g.retryAfter)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":{"message":"rejected by test gate"}}`))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// isToolCall 判断请求体是不是一次 tools/call。
func isToolCall(body []byte) bool {
	return bytes.Contains(body, []byte(`"method":"tools/call"`)) ||
		bytes.Contains(body, []byte(`"method": "tools/call"`))
}

func startKeyedDownstream(t *testing.T, gate *keyedGate, tools ...toolSpec) string {
	t.Helper()
	httpSrv := httptest.NewServer(gate.wrap(newMCPHandler(tools)))
	t.Cleanup(httpSrv.Close)
	return httpSrv.URL
}

// addCredentials 往一个 server 挂上若干把密钥，并让它们立即生效。
func addCredentials(t *testing.T, e *env, serverKey string, tokens ...string) []credentials.Credential {
	t.Helper()

	serverID := e.serverIDOf(t, serverKey)
	ctx := context.Background()

	out := make([]credentials.Credential, 0, len(tokens))
	for i, tok := range tokens {
		saved, err := e.store.Credentials.Insert(ctx, credentials.Credential{
			ServerID: serverID,
			Label:    fmt.Sprintf("密钥 %d", i+1),
			Headers:  map[string]string{"Authorization": "Bearer " + tok},
			Enabled:  true,
		})
		if err != nil {
			t.Fatalf("写入密钥失败: %v", err)
		}
		out = append(out, saved)
	}

	if err := e.svc.Reload(ctx); err != nil {
		t.Fatalf("重载失败: %v", err)
	}
	return out
}

// specOf 取某个 server 当前的配置。
func specOf(t *testing.T, e *env, serverKey string) downstream.Spec {
	t.Helper()
	sv, err := e.store.Servers.GetByKey(context.Background(), serverKey)
	if err != nil {
		t.Fatalf("查 server %s 失败: %v", serverKey, err)
	}
	return sv.Spec
}

// --- 429：换一把密钥，调用照常成功 ---

// 断言的是"调用方看不到限流"这件事，而不是"第几次用了哪把密钥"。
//
// 轮换游标的起点在网关内部（装载时的工具刷新也会借走一把并推进它），
// 把测试钉在"第一把必定是先插入的那把"上，会变成一个只要刷新时机变
// 就碎的用例——而它真正要守的性质（限流对调用方无感）从未受威胁。
func TestKeyPoolSwitchesKeyOnRateLimit(t *testing.T) {
	gate := newKeyedGate("30")
	url := startKeyedDownstream(t, gate, echoTool("ping"))

	spec := authSpec("pool", url, "bootstrap")
	e := newEnv(t, []downstream.Spec{spec})
	addCredentials(t, e, "pool", "limited", "healthy")
	gate.deny("limited", http.StatusTooManyRequests)

	gate.reset()
	// 轮换会覆盖池子里的每一把，多打几次确保被限流的那把一定被选到过。
	for i := 0; i < 4; i++ {
		res := e.callTool(t, "pool__ping", `{"v":1}`)
		if res.IsError {
			t.Fatalf("第 %d 次调用失败：限流没有对调用方隐藏掉 —— %s", i+1, textOf(t, res))
		}
	}

	// 被限流的那把只挨过一次：它在 429 之后进入冷却，后续调用绕开它。
	// 若冷却没生效，这个数字会随调用次数一起涨。
	if n := gate.Calls("limited"); n != 1 {
		t.Errorf("被限流的那把被用了 %d 次, want 1：冷却没有生效", n)
	}

	// 冷却中的密钥在界面上要看得见，否则"为什么流量都压在一把上"无从解释。
	var cooled bool
	for _, st := range e.svc.Pool().KeyStatusesOf(e.serverIDOf(t, "pool")) {
		if st.Label == "密钥 1" && st.State == downstream.KeyCooling {
			cooled = true
		}
	}
	if !cooled {
		t.Error("被限流的密钥没有进入冷却状态")
	}
}

// --- 多把密钥大面积限流：只要还有一把能用，调用方就看不到失败 ---

// 预算必须覆盖池子里的全部可用密钥：4 把被限流、1 把健康时，无论
// 轮换游标从哪把开始，这一次调用最坏要把 4 把坏的全试一遍再摸到
// 健康的那把。若预算被截断在 3，游标恰好从坏密钥排起时调用方就会
// 看到 429——而池子里明明还有可用的密钥。
func TestKeyPoolTransparentWhenAnyKeyUsable(t *testing.T) {
	gate := newKeyedGate("30")
	url := startKeyedDownstream(t, gate, echoTool("ping"))

	spec := authSpec("pool", url, "bootstrap")
	e := newEnv(t, []downstream.Spec{spec})
	addCredentials(t, e, "pool", "bad1", "bad2", "bad3", "bad4", "good")
	for _, tok := range []string{"bad1", "bad2", "bad3", "bad4"} {
		gate.deny(tok, http.StatusTooManyRequests)
	}

	gate.reset()
	// 连打 6 次，覆盖轮换游标的各个起点（含最坏情况：4 把坏密钥
	// 全部排在健康密钥之前）。任何一次失败都违反"有可用密钥时
	// 调用方无感"。
	for i := 0; i < 6; i++ {
		res := e.callTool(t, "pool__ping", `{"v":1}`)
		if res.IsError {
			t.Fatalf("第 %d 次调用失败：池里还有可用密钥，限流却没有对调用方隐藏 —— %s",
				i+1, textOf(t, res))
		}
	}

	// 健康的那把至少被用到一次；坏密钥只挨冷却，不被弃用。
	if n := gate.Calls("good"); n == 0 {
		t.Error("健康的密钥一次都没被用到：轮换没有兜住")
	}
	for _, st := range e.svc.Pool().KeyStatusesOf(e.serverIDOf(t, "pool")) {
		if st.State == downstream.KeyDisabled {
			t.Errorf("限流把密钥 %s 弃用了：429 只该进冷却", st.Label)
		}
	}
}

// --- 401：弃用该密钥，并且后续永不使用 ---

func TestKeyPoolDisablesInvalidKey(t *testing.T) {
	gate := newKeyedGate("")
	url := startKeyedDownstream(t, gate, echoTool("ping"))

	spec := authSpec("pool", url, "bootstrap")
	e := newEnv(t, []downstream.Spec{spec})
	creds := addCredentials(t, e, "pool", "dead", "alive")
	gate.deny("dead", http.StatusUnauthorized)

	res := e.callTool(t, "pool__ping", `{}`)
	if res.IsError {
		t.Fatalf("遇到无效密钥后没有换一把: %s", textOf(t, res))
	}

	// 关键是"永不再用"：不只是一次调用内换掉，而是持久弃用。
	for i := 0; i < 4; i++ {
		e.callTool(t, "pool__ping", `{}`)
	}
	if n := gate.Calls("dead"); n != 1 {
		t.Errorf("已被弃用的密钥又被用了 %d 次", n)
	}

	// 落库核对：弃用是持久事实，重启后不该重踩同一个坑。
	saved, err := e.store.Credentials.Get(context.Background(), creds[0].ID)
	if err != nil {
		t.Fatalf("回读密钥失败: %v", err)
	}
	if saved.State != credentials.CredStateDisabled {
		t.Errorf("密钥状态 = %q, want disabled", saved.State)
	}
	if saved.DisabledKind != downstream.VerdictInvalidKey {
		t.Errorf("弃用原因 = %q, want INVALID_KEY", saved.DisabledKind)
	}

	// 重启语义：新进程装载同一份库，仍然不会用它。
	if err := e.svc.Reload(context.Background()); err != nil {
		t.Fatalf("重载失败: %v", err)
	}
	gate.reset()
	e.callTool(t, "pool__ping", `{}`)
	if n := gate.Calls("dead"); n != 0 {
		t.Errorf("重载之后又用了已知无效的密钥 %d 次", n)
	}
}

// --- 429 且无可用替补：快速失败并给出 retryAfter ---

func TestKeyPoolAllCoolingSurfacesUnavailable(t *testing.T) {
	gate := newKeyedGate("20")
	url := startKeyedDownstream(t, gate, echoTool("ping"))

	spec := authSpec("pool", url, "bootstrap")
	e := newEnv(t, []downstream.Spec{spec})
	addCredentials(t, e, "pool", "k1", "k2")
	gate.deny("k1", http.StatusTooManyRequests)
	gate.deny("k2", http.StatusTooManyRequests)

	_, err := e.svc.Pool().CallTool(context.Background(), specOf(t, e, "pool"), "ping", nil)
	if err == nil {
		t.Fatal("全部密钥被限流时调用却成功了")
	}

	var noKey *downstream.ErrNoUsableKey
	if !errors.As(err, &noKey) {
		t.Fatalf("错误类型 = %T(%v), want *ErrNoUsableKey", err, err)
	}
	if noKey.Cooling != 2 {
		t.Errorf("冷却计数 = %d, want 2: %+v", noKey.Cooling, noKey)
	}
	if noKey.RetryAfter < 15*time.Second {
		t.Errorf("retryAfter = %s, 应当来自下游声明的 20s", noKey.RetryAfter)
	}

	// 快速失败的含义：不再往下游打请求。三次调用之后下游收到的
	// 调用数应当停在 2（每把各挨一次），而不是随重试增长。
	before := gate.Calls("k1") + gate.Calls("k2")
	for i := 0; i < 3; i++ {
		_, _ = e.svc.Pool().CallTool(context.Background(), specOf(t, e, "pool"), "ping", nil)
	}
	if after := gate.Calls("k1") + gate.Calls("k2"); after != before {
		t.Errorf("冷却期间下游调用数 %d → %d：快速失败没有生效", before, after)
	}
}

// --- 调用进行中池子被打空：上抛的必须是"无可用密钥"，不是裸 429 ---

// 把预算调得比池子大，让"借不到密钥"发生在循环中间而非预算耗尽后：
// k1 429 → k2 429 → 第三把借不到。此刻调用方能拿到的唯一正确答复是
// ErrNoUsableKey（带 retryAfter 与失败原因）——"等 20 秒"；而裸 429
// 只描述了最后一把的遭遇，丢掉了"两把都在冷却"这个更关键的事实。
func TestKeyPoolMidLoopDrainSurfacesNoUsableKey(t *testing.T) {
	gate := newKeyedGate("20")
	url := startKeyedDownstream(t, gate, echoTool("ping"))

	spec := authSpec("pool", url, "bootstrap")
	e := newEnv(t, []downstream.Spec{spec})
	addCredentials(t, e, "pool", "k1", "k2")
	gate.deny("k1", http.StatusTooManyRequests)
	gate.deny("k2", http.StatusTooManyRequests)

	e.svc.Pool().MaxAttempts = 5 // 预算大于池子：第三次 Acquire 必然借不到

	_, err := e.svc.Pool().CallTool(context.Background(), specOf(t, e, "pool"), "ping", nil)
	if err == nil {
		t.Fatal("全部密钥被限流时调用却成功了")
	}

	var noKey *downstream.ErrNoUsableKey
	if !errors.As(err, &noKey) {
		t.Fatalf("错误类型 = %T(%v), want *ErrNoUsableKey：调用方看到的必须是带等待时长的池级结论", err, err)
	}
	if noKey.Cooling != 2 {
		t.Errorf("冷却计数 = %d, want 2: %+v", noKey.Cooling, noKey)
	}
	if noKey.RetryAfter < 15*time.Second {
		t.Errorf("retryAfter = %s, 应当来自下游声明的 20s", noKey.RetryAfter)
	}
	if noKey.Cause == nil {
		t.Error("Cause 为空：调用方只看到池空了，不知道为什么空")
	}
}

// --- 5xx 与超时：密钥状态零变化，且不重试 ---

func TestKeyPoolNeutralFailureDoesNotTouchKeys(t *testing.T) {
	gate := newKeyedGate("")
	url := startKeyedDownstream(t, gate, echoTool("ping"))

	spec := authSpec("pool", url, "bootstrap")
	e := newEnv(t, []downstream.Spec{spec})
	creds := addCredentials(t, e, "pool", "k1", "k2")
	gate.deny("k1", http.StatusInternalServerError)
	gate.deny("k2", http.StatusInternalServerError)

	_, err := e.svc.Pool().CallTool(context.Background(), specOf(t, e, "pool"), "ping", nil)
	if err == nil {
		t.Fatal("下游 5xx 时调用却成功了")
	}

	// 5xx 与密钥无关，绝不能因此把密钥打进冷却或弃用——
	// 那会让一次下游抖动演变成"所有密钥都不可用"。
	for _, c := range creds {
		saved, err := e.store.Credentials.Get(context.Background(), c.ID)
		if err != nil {
			t.Fatalf("回读密钥失败: %v", err)
		}
		if saved.State == credentials.CredStateDisabled {
			t.Errorf("5xx 把密钥 %s 弃用了", saved.Label)
		}
	}
	statuses := e.svc.Pool().KeyStatusesOf(e.serverIDOf(t, "pool"))
	for _, st := range statuses {
		if st.State != downstream.KeyActive {
			t.Errorf("5xx 之后密钥 %s 的状态 = %s, want active", st.Label, st.State)
		}
	}

	// 不重试：5xx 可能已经执行过，重试会让有副作用的工具跑第二遍。
	// 两把密钥各挨一次即可说明问题。
	if total := gate.Calls("k1") + gate.Calls("k2"); total > 1 {
		t.Errorf("5xx 触发了重试（下游收到 %d 次调用）", total)
	}
}

// --- 并发归因：被冷却的必须是收到 429 的那一把 ---

func TestKeyPoolConcurrentAttribution(t *testing.T) {
	gate := newKeyedGate("30")
	url := startKeyedDownstream(t, gate, slowTool("work", 30*time.Millisecond))

	spec := authSpec("pool", url, "bootstrap")
	e := newEnv(t, []downstream.Spec{spec})
	addCredentials(t, e, "pool", "bad", "good")
	gate.deny("bad", http.StatusTooManyRequests)

	specCur := specOf(t, e, "pool")

	// 并发发起：如果观测用"最近一次往返"来归类，A 收到的 429
	// 会被读到 B 头上，症状是把无辜的密钥封掉。
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = e.svc.Pool().CallTool(context.Background(), specCur, "work", nil)
		}()
	}
	wg.Wait()

	statuses := e.svc.Pool().KeyStatusesOf(e.serverIDOf(t, "pool"))
	byLabel := map[string]downstream.KeyStatus{}
	for _, st := range statuses {
		byLabel[st.Label] = st
	}

	// "good" 一次也不该被限流：它没有收到过 429。
	good, ok := byLabel["密钥 2"]
	if !ok {
		t.Fatalf("取不到第二把密钥的状态: %+v", statuses)
	}
	if good.State == downstream.KeyCooling {
		t.Errorf("无辜的密钥被冷却了：429 的归因串到了另一把上（并发观测槽位没生效）")
	}
	if good.LastError != "" {
		t.Errorf("无辜的密钥被记了错: %q", good.LastError)
	}
}

// --- 密钥池打空后，AI 读到的那段文字必须是可执行的 ---

// 这段文字是调用方唯一能读到的"该怎么办"。退化成一个分类名或一句
// "未知错误"，等于把"等 20 秒"这个唯一有用的结论丢掉。
func TestNoUsableKeyAdviceIsActionable(t *testing.T) {
	gate := newKeyedGate("20")
	url := startKeyedDownstream(t, gate, echoTool("ping"))

	spec := authSpec("pool", url, "bootstrap")
	e := newEnv(t, []downstream.Spec{spec})
	addCredentials(t, e, "pool", "k1")
	gate.deny("k1", http.StatusTooManyRequests)

	res := e.callTool(t, "pool__ping", `{}`)
	if !res.IsError {
		t.Fatal("唯一一把密钥被限流时调用却成功了")
	}

	text := textOf(t, res)
	if strings.Contains(text, "未知错误") {
		t.Errorf("分类退化成了未知错误:\n%s", text)
	}
	if !strings.Contains(text, "没有可用密钥") {
		t.Errorf("没有说清是密钥池空了:\n%s", text)
	}
	if !strings.Contains(text, "冷却") {
		t.Errorf("没有说清为什么不可用:\n%s", text)
	}
	if !strings.Contains(text, "配额限制") {
		t.Errorf("给的建议不对——应当是\"稍后重试\"而不是\"改用其他工具\":\n%s", text)
	}
}

// --- 用量的密钥归因：日志里能看到是哪一把 ---

func TestKeyPoolLogsCredentialID(t *testing.T) {
	gate := newKeyedGate("")
	url := startKeyedDownstream(t, gate, echoTool("ping"))

	spec := authSpec("pool", url, "bootstrap")
	e := newEnv(t, []downstream.Spec{spec})
	creds := addCredentials(t, e, "pool", "only-one")

	e.callTool(t, "pool__ping", `{}`)

	// 日志是异步落库的，等它冲一批。
	var entries []logs.LogEntry
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		entries, _, _ = e.store.Logs.List(context.Background(), logs.LogQuery{Limit: 20})
		if len(entries) > 0 {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}

	if len(entries) == 0 {
		t.Fatal("没有调用日志")
	}
	if entries[0].CredentialID != creds[0].ID {
		t.Errorf("日志的 credential_id = %q, want %q：没有它就无法回答"+
			"这把密钥为什么失败了",
			entries[0].CredentialID, creds[0].ID)
	}
}

// --- 单密钥模式回归：没配密钥池时行为一字不变 ---

func TestSingleKeyModeUnchanged(t *testing.T) {
	gate := newKeyedGate("")
	url := startKeyedDownstream(t, gate, echoTool("ping"))

	spec := authSpec("single", url, "static-token")
	e := newEnv(t, []downstream.Spec{spec})

	// 没有 credentials：沿用 servers.headers。
	res := e.callTool(t, "single__ping", `{"v":1}`)
	if res.IsError {
		t.Fatalf("单密钥模式的调用失败: %s", textOf(t, res))
	}
	if creds := gate.Credentials(); len(creds) == 0 || creds[len(creds)-1] != "Bearer static-token" {
		t.Errorf("单密钥模式没有沿用 headers: %v", creds)
	}

	// 密钥池为空，而不是"有一个空池子"：这个区别决定了界面上
	// 显示的是"单密钥模式"还是"密钥池有 0 把"。
	if st := e.svc.Pool().KeyStatusesOf(e.serverIDOf(t, "single")); len(st) != 0 {
		t.Errorf("单密钥模式下不该有密钥池状态: %+v", st)
	}

	// 失败路径同样不变：401 时调用失败，而不是去找一把不存在的替补。
	gate.deny("static-token", http.StatusUnauthorized)
	res = e.callTool(t, "single__ping", `{}`)
	if !res.IsError {
		t.Error("单密钥模式下 401 居然成功了")
	}
}

// --- 探活与调用共用密钥池 ---

// startStrictAuthDownstream 拒绝所有未带指定凭据的请求（包括握手）。
// 与 keyedGate 只拦 tools/call 不同，它能证明探活本身带了池里的密钥。
func startStrictAuthDownstream(t *testing.T, token string, tools ...toolSpec) string {
	t.Helper()
	base := newMCPHandler(tools)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		base.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// 只配密钥池、servers.headers 为空的 server，探活也必须能过。
//
// 探活走 servers.headers 的旧行为会在这里稳定 401：状态被判 offline，
// 连续几次后还把整个 server 拦进 blockGate，池里健康的密钥完全用不上。
func TestProbeUsesKeyPoolCredentials(t *testing.T) {
	url := startStrictAuthDownstream(t, "good", echoTool("ping"))
	e := newEnv(t, []downstream.Spec{remoteSpec("probe-pool", url)})
	addCredentials(t, e, "probe-pool", "good")

	report := e.svc.RefreshAll(context.Background())
	if report.Total != 1 || report.Healthy != 1 {
		t.Fatalf("探活没有使用密钥池凭据: %+v", report.Results)
	}
	if len(report.Results) != 1 || !report.Results[0].OK {
		t.Fatalf("探活结果 = %+v, want OK", report.Results)
	}

	tools, err := e.store.Servers.ListTools(context.Background())
	if err != nil {
		t.Fatalf("读取工具缓存失败: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "ping" {
		t.Fatalf("工具缓存 = %+v, want [ping]", tools)
	}
}
