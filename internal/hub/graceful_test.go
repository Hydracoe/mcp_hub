package hub_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hydra/mcp-hub/internal/downstream"
	"github.com/hydra/mcp-hub/internal/store"
	"github.com/mark3labs/mcp-go/mcp"
)

// authGate 是包在下游外面的凭据校验层。
//
// 它同时干三件事：拒绝错误凭据、记下每次请求带的凭据、统计握手次数。
// 三样都是"从外部观察网关行为"的证据，比断言网关内部字段可靠——
// 内部字段对不对不是重点，请求到底带出去什么才是。
type authGate struct {
	accept atomic.Value // string：当前接受的密钥

	// delay 是拒绝之前先拖多久。用来制造"请求在路上时凭据被换掉"
	// 那个瞬间——没有它，401 会在测试有机会动手之前就返回，
	// 那条重试路径根本覆盖不到。
	delay atomic.Int64

	handshakes atomic.Int64

	mu   sync.Mutex
	seen []string
}

func newAuthGate(token string) *authGate {
	g := &authGate{}
	g.accept.Store(token)
	return g
}

func (g *authGate) SetToken(token string) { g.accept.Store(token) }

func (g *authGate) SetDelay(d time.Duration) { g.delay.Store(int64(d)) }

// Handshakes 返回下游收到的握手请求数。
//
// 这是"连接有没有被重建"最直接的证据：重建必然重新握手。
func (g *authGate) Handshakes() int { return int(g.handshakes.Load()) }

// Credentials 返回下游依次收到的 Authorization 头。
func (g *authGate) Credentials() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.seen...)
}

func (g *authGate) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get("Authorization")

		body, err := io.ReadAll(r.Body)
		if err == nil {
			// 读完要放回去，否则后面的 handler 收到空 body。
			r.Body = io.NopCloser(bytes.NewReader(body))
			if isHandshake(body) {
				g.handshakes.Add(1)
			}
		}

		g.mu.Lock()
		g.seen = append(g.seen, got)
		g.mu.Unlock()

		want, _ := g.accept.Load().(string)
		if got != "Bearer "+want {
			if d := time.Duration(g.delay.Load()); d > 0 {
				time.Sleep(d)
			}
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// isHandshake 判断一个请求体是不是协议握手。
//
// 新版走 server/discover，旧版走 initialize，两种都要认——
// 否则"连接重建了吗"这个问题会取决于下游协商到了哪一代协议。
func isHandshake(body []byte) bool {
	return bytes.Contains(body, []byte("server/discover")) ||
		bytes.Contains(body, []byte(`"initialize"`))
}

// startGatedDownstream 起一个会校验凭据的下游。
func startGatedDownstream(t *testing.T, gate *authGate, tools ...toolSpec) string {
	t.Helper()
	httpSrv := httptest.NewServer(gate.wrap(newMCPHandler(tools)))
	t.Cleanup(httpSrv.Close)
	return httpSrv.URL
}

// slowTool 是一个耗时可控的工具，用来制造"正在跑的调用"。
func slowTool(name string, d time.Duration) toolSpec {
	return toolSpec{
		name: name,
		desc: "answers after a delay",
		handler: func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			select {
			case <-time.After(d):
				return mcp.NewToolResultText("done"), nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
	}
}

// authSpec 造一个带凭据的远程配置。
func authSpec(key, url, token string) downstream.Spec {
	spec := remoteSpec(key, url)
	spec.Headers = map[string]string{"Authorization": "Bearer " + token}
	return spec
}

// asyncCall 在后台发起一次工具调用，把结果送进返回的 channel。
func (e *env) asyncCall(name string) <-chan callOutcome {
	ch := make(chan callOutcome, 1)
	go func() {
		req := mcp.CallToolRequest{}
		req.Params.Name = name
		req.Params.RawArguments = json.RawMessage(`{}`)
		res, err := e.client.CallTool(context.Background(), req)
		ch <- callOutcome{res: res, err: err}
	}()
	return ch
}

type callOutcome struct {
	res *mcp.CallToolResult
	err error
}

// serverIDOf 取某个 key 对应的 serverId。
func (e *env) serverIDOf(t *testing.T, key string) string {
	t.Helper()
	sv, err := e.store.GetServerByKey(context.Background(), key)
	if err != nil {
		t.Fatalf("查 server %s 失败: %v", key, err)
	}
	return sv.Spec.ID
}

// apply 用一份新配置覆盖已有 server，走与 HTTP 接口同一条路径。
func (e *env) apply(t *testing.T, spec downstream.Spec) {
	t.Helper()
	if _, err := e.svc.ApplyServer(context.Background(),
		store.Config{Spec: spec, Enabled: true}); err != nil {
		t.Fatalf("应用配置失败: %v", err)
	}
}

// --- 凭据轮换 ---

// 轮换密钥不该被调用方感知：连接不重建，在飞调用不被打断，
// 后续请求自动带上新凭据。
func TestCredentialRotationKeepsConnectionAndInflightCall(t *testing.T) {
	gate := newAuthGate("old")
	url := startGatedDownstream(t, gate, slowTool("slow", 400*time.Millisecond))

	spec := authSpec("vault", url, "old")
	e := newEnv(t, []downstream.Spec{spec})

	// 先跑一次，让常驻连接建起来，再采样握手次数作为基准。
	e.callTool(t, "vault__slow", "")
	handshakes := gate.Handshakes()

	pending := e.asyncCall("vault__slow")
	time.Sleep(120 * time.Millisecond)

	// 下游与网关同时切到新密钥。
	gate.SetToken("new")
	e.apply(t, authSpec("vault", url, "new"))

	got := <-pending
	if got.err != nil {
		t.Fatalf("轮换密钥打断了在飞调用: %v", got.err)
	}
	if got.res.IsError {
		t.Fatalf("在飞调用被标记为失败: %+v", got.res)
	}

	if now := gate.Handshakes(); now != handshakes {
		t.Errorf("握手次数 %d → %d：连接被重建了，轮换凭据不该付这个代价",
			handshakes, now)
	}

	// 后续请求必须带上新凭据，否则轮换等于没生效。
	e.callTool(t, "vault__slow", "")
	creds := gate.Credentials()
	if last := creds[len(creds)-1]; last != "Bearer new" {
		t.Errorf("轮换后的请求仍带旧凭据: %q", last)
	}
}

// 凭据被下游吊销、而网关恰好在请求往返期间换上新凭据时，
// 这一次调用应当自动重试成功，调用方看不到任何失败。
func TestStaleCredentialIsRetriedAfterRotation(t *testing.T) {
	gate := newAuthGate("old")
	url := startGatedDownstream(t, gate, echoTool("ping"))

	spec := authSpec("vault", url, "old")
	e := newEnv(t, []downstream.Spec{spec})

	e.callTool(t, "vault__ping", "")
	before := len(gate.Credentials())

	// 下游吊销旧密钥，并让拒绝慢下来，好让轮换发生在 401 返回之前。
	gate.SetToken("new")
	gate.SetDelay(300 * time.Millisecond)

	pending := e.asyncCall("vault__ping")
	time.Sleep(100 * time.Millisecond)
	e.apply(t, authSpec("vault", url, "new"))

	got := <-pending
	if got.err != nil {
		t.Fatalf("轮换期间调用失败，说明没有用新凭据重试: %v", got.err)
	}
	if got.res.IsError {
		t.Fatalf("轮换期间调用被标记为失败: %+v", got.res)
	}

	// 下游应当看到两次尝试：先旧凭据被拒，再新凭据成功。
	creds := gate.Credentials()[before:]
	if len(creds) != 2 {
		t.Fatalf("下游收到 %d 次请求, want 2（一次被拒 + 一次重试）: %q", len(creds), creds)
	}
	if creds[0] != "Bearer old" || creds[1] != "Bearer new" {
		t.Errorf("重试序列 = %q, want [旧 新]", creds)
	}
}

// 凭据没变过时不该重试：那只是配置错了，重试只会多打一次无效请求，
// 还会把"密钥填错了"这个明确信号糊成"偶发失败"。
func TestAuthFailureIsNotRetriedWhenCredentialUnchanged(t *testing.T) {
	gate := newAuthGate("old")
	url := startGatedDownstream(t, gate, echoTool("ping"))

	spec := authSpec("vault", url, "old")
	e := newEnv(t, []downstream.Spec{spec})

	e.callTool(t, "vault__ping", "")
	before := len(gate.Credentials())

	// 下游悄悄吊销旧密钥，网关并不知情。
	gate.SetToken("new")

	res := e.callTool(t, "vault__ping", "")
	if !res.IsError {
		t.Fatalf("预期调用失败，实际成功: %+v", res)
	}
	if got := len(gate.Credentials()) - before; got != 1 {
		t.Errorf("下游收到 %d 次请求, want 1（凭据没变就不该重试）", got)
	}
}

// --- 优雅停用 ---

// 停用要分两步落地：工具立刻从聚合列表撤下，在飞调用照常跑完。
func TestDisableWaitsForInflightCall(t *testing.T) {
	const work = 500 * time.Millisecond

	url := startDownstream(t, slowTool("slow", work))
	e := newEnv(t, []downstream.Spec{remoteSpec("slow-node", url)})

	// 先调用一次，确认它已出现在聚合列表里。
	e.callTool(t, "slow-node__slow", "")
	if !slices.Contains(toolNames(e.listTools(t)), "slow-node__slow") {
		t.Fatal("停用前工具就不在列表里")
	}

	started := time.Now()
	pending := e.asyncCall("slow-node__slow")
	time.Sleep(150 * time.Millisecond)

	if err := e.svc.SetEnabled(context.Background(), e.serverIDOf(t, "slow-node"), false); err != nil {
		t.Fatalf("停用失败: %v", err)
	}

	// 摘除是立刻的：从这一刻起 AI 不会再看到这个工具。
	if slices.Contains(toolNames(e.listTools(t)), "slow-node__slow") {
		t.Error("停用后工具仍出现在聚合列表里")
	}

	// 等待是耐心的：正在跑的那次调用必须完整跑完。
	got := <-pending
	if got.err != nil {
		t.Fatalf("停用打断了在飞调用: %v", got.err)
	}
	if got.res.IsError {
		t.Fatalf("在飞调用被标记为失败: %+v", got.res)
	}
	if elapsed := time.Since(started); elapsed < work {
		t.Errorf("调用耗时 %v < %v，看起来被提前掐断了", elapsed, work)
	}
}

// 等到上限还没收尾时必须强关，否则管理界面上那个 server 会永远停在
// "停用中"，而用户没有任何办法——这是"无限等待"方案的死结。
func TestDisableForceClosesAfterDrainTimeout(t *testing.T) {
	url := startDownstream(t, slowTool("stuck", 10*time.Second))
	e := newEnv(t, []downstream.Spec{remoteSpec("stuck-node", url)})

	const limit = 300 * time.Millisecond
	e.svc.Pool().DrainTimeout = limit

	pending := e.asyncCall("stuck-node__stuck")
	time.Sleep(100 * time.Millisecond)

	started := time.Now()
	if err := e.svc.SetEnabled(context.Background(), e.serverIDOf(t, "stuck-node"), false); err != nil {
		t.Fatalf("停用失败: %v", err)
	}

	select {
	case got := <-pending:
		if got.err != nil {
			t.Fatalf("协议层错误不该冒到调用方: %v", got.err)
		}
		if !got.res.IsError {
			t.Fatalf("超时强关后调用应当失败，实际成功: %+v", got.res)
		}
		if elapsed := time.Since(started); elapsed < limit {
			t.Errorf("调用在 %v 就结束了，早于等待上限 %v", elapsed, limit)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("超时强关没有生效：调用一直挂着，停用永远完不成")
	}
}

// 停用后连接要真的退场：再查管理接口时不该还显示"停用中"。
func TestDrainFinishesAndClearsState(t *testing.T) {
	url := startDownstream(t, slowTool("slow", 300*time.Millisecond))
	e := newEnv(t, []downstream.Spec{remoteSpec("node", url)})

	e.callTool(t, "node__slow", "")

	pending := e.asyncCall("node__slow")
	time.Sleep(80 * time.Millisecond)

	id := e.serverIDOf(t, "node")
	if err := e.svc.SetEnabled(context.Background(), id, false); err != nil {
		t.Fatalf("停用失败: %v", err)
	}

	// 收尾期间应当能看到进度，界面上才有东西可显示。
	if info, ok := e.svc.DrainingInfo(id); !ok {
		t.Error("收尾期间查不到进度，界面只能显示「已停用」")
	} else if info.Inflight < 1 {
		t.Errorf("在飞调用数 = %d, want >= 1", info.Inflight)
	}

	<-pending

	// 收尾完成后状态要清干净。
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := e.svc.DrainingInfo(id); !ok {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Error("收尾已经结束，但停用状态一直没清掉")
}

// --- 目标变更 ---

// 真的换了目标就必须重建连接，否则请求会打到上一个地址上——
// 这类故障最难查，因为一切看起来都正常。
func TestTargetChangeRebuildsConnection(t *testing.T) {
	urlA := startDownstream(t, taggedTool("which", "A"))
	urlB := startDownstream(t, taggedTool("which", "B"))

	spec := remoteSpec("node", urlA)
	e := newEnv(t, []downstream.Spec{spec})

	if got := textOf(t, e.callTool(t, "node__which", "")); got != "A" {
		t.Fatalf("初始目标返回 %q, want A", got)
	}

	changed := spec
	changed.URL = urlB
	e.apply(t, changed)

	if got := textOf(t, e.callTool(t, "node__which", "")); got != "B" {
		t.Errorf("改地址后仍打到旧目标: %q", got)
	}
}

// 改一个不影响连接的字段（显示名）不该重建连接。
func TestMetadataChangeKeepsConnection(t *testing.T) {
	gate := newAuthGate("tok")
	url := startGatedDownstream(t, gate, echoTool("ping"))

	spec := authSpec("node", url, "tok")
	e := newEnv(t, []downstream.Spec{spec})

	e.callTool(t, "node__ping", "")
	handshakes := gate.Handshakes()

	renamed := spec
	renamed.Name = "换个显示名"
	e.apply(t, renamed)

	if now := gate.Handshakes(); now != handshakes {
		t.Errorf("握手次数 %d → %d：改显示名不该重建连接", handshakes, now)
	}
}

// 指纹必须能把"换目标"和"换元数据"分开，这是上面两条行为的地基。
func TestConnFingerprintSeparatesMetadataFromTarget(t *testing.T) {
	base := downstream.Spec{
		Key:       "k",
		Name:      "n",
		Transport: downstream.TransportStreamableHTTP,
		URL:       "https://example.com/mcp",
		Headers:   map[string]string{"Authorization": "Bearer a"},
	}

	renamed := base
	renamed.Name = "另一个名字"
	if base.ConnFingerprint() != renamed.ConnFingerprint() {
		t.Error("改显示名不该影响连接指纹")
	}

	rotated := base
	rotated.Headers = map[string]string{"Authorization": "Bearer b"}
	if base.ConnFingerprint() != rotated.ConnFingerprint() {
		t.Error("换凭据不该影响连接指纹：它按请求注入，不需要重建连接")
	}

	moved := base
	moved.URL = "https://example.com/other"
	if base.ConnFingerprint() == moved.ConnFingerprint() {
		t.Error("改地址必须改变连接指纹，否则请求会打到旧目标")
	}

	timedOut := base
	timedOut.Timeout = 99 * time.Second
	if base.ConnFingerprint() == timedOut.ConnFingerprint() {
		t.Error("改超时必须改变连接指纹：超时是构造时固定的")
	}
}

// 没有在飞调用的停用应当立刻完成，不该让界面闪一下"停用中"。
func TestDisableWithoutInflightIsImmediate(t *testing.T) {
	url := startDownstream(t, echoTool("ping"))
	e := newEnv(t, []downstream.Spec{remoteSpec("node", url)})

	e.callTool(t, "node__ping", "")

	id := e.serverIDOf(t, "node")
	if err := e.svc.SetEnabled(context.Background(), id, false); err != nil {
		t.Fatalf("停用失败: %v", err)
	}

	if info, ok := e.svc.DrainingInfo(id); ok {
		t.Errorf("没有在飞调用却在收尾: %+v", info)
	}
}
