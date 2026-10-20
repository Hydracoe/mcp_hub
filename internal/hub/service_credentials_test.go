package hub

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hydra/mcp-hub/internal/cache/cachetest"
	"github.com/hydra/mcp-hub/internal/downstream"
	"github.com/hydra/mcp-hub/internal/store"
	"github.com/hydra/mcp-hub/internal/store/servers"
	"github.com/hydra/mcp-hub/internal/store/storetest"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// 凭据试测的回归测试。
//
// 批量导入一次可能带来几十把密钥，每把都要试测一次，而试测是真的会连一次
// 下游。早期实现是"每把密钥起一个 goroutine 去试测"：导入 20 把就是 20 个
// 探活同时打向下游，既是突发流量（可能触发限流甚至封禁），也让"到底哪一把
// 出问题"难以分辨。这里的用例盯住三件事：
//
//  1. 同时在途的探活数有上限（TestImportCredentialsProbeConcurrencyIsBounded）；
//  2. 试测失败不牵连整批导入，成功的那几把照样写进库与密钥池
//     （TestImportCredentialsProbeFailureKeepsBatchIntact）；
//  3. 单条新增路径的语义没被改坏（TestAddCredentialProbesExactlyOnce）。

// newCredentialTestSvc 起一套跑得起来的网关（真实库 + 内存缓存），
// 并返回一个可断言日志的 observer。
func newCredentialTestSvc(t *testing.T) (*Service, *store.Store, *observer.ObservedLogs) {
	t.Helper()

	st, err := store.Open(storetest.DSN(t))
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	core, logs := observer.New(zap.InfoLevel)
	svc := NewServiceWithCache(st, zap.New(core), cachetest.New())
	t.Cleanup(func() { svc.Close() })

	return svc, st, logs
}

// seedRemoteServer 写一个远程 server（密钥池只支持远程传输）。
// URL 指向一个没人监听的端口：试测被假实现替换了，不会真的去连。
func seedRemoteServer(t *testing.T, st *store.Store, key string) string {
	t.Helper()

	spec := downstream.Spec{
		ID:        "srv-" + key,
		Key:       key,
		Name:      key,
		Transport: downstream.TransportStreamableHTTP,
		URL:       "http://127.0.0.1:1/mcp",
		Timeout:   5 * time.Second,
	}
	if err := st.Servers.Upsert(context.Background(), servers.Config{Spec: spec, Enabled: true}); err != nil {
		t.Fatalf("写入 server %s 失败: %v", key, err)
	}
	return spec.ID
}

// importSpecs 造 n 把互不相同的密钥（值不同才不会撞上同值检测）。
func importSpecs(n int) []CredentialSpec {
	out := make([]CredentialSpec, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, CredentialSpec{
			Label:   fmt.Sprintf("key-%02d", i),
			Headers: map[string]string{"Authorization": fmt.Sprintf("Bearer token-%02d", i)},
		})
	}
	return out
}

// 批量导入的试测必须有并发上限。
//
// 假探活把每个探活都挂住，直到"这一批全部到位"才放行：
//   - 不设上限时，8 个探活会同时在途（峰值 8 > 上限），用例失败；
//   - 设了上限时，最多 credentialTestConcurrency 个能同时进门，其余的
//     根本进不来，"全部到位"永远凑不齐——只能等试测超时收场。
//
// 峰值由假探活自己数（进入时加一、退出时减一，取历史最大值），不靠 sleep
// 猜并发。
func TestImportCredentialsProbeConcurrencyIsBounded(t *testing.T) {
	const total = 8

	svc, st, _ := newCredentialTestSvc(t)
	ctx := context.Background()
	serverID := seedRemoteServer(t, st, "probesrv")

	var (
		mu       sync.Mutex
		inFlight int
		peak     int
		entered  int
	)
	allIn := make(chan struct{})
	finished := make(chan struct{}, total)

	svc.probeFn = func(pctx context.Context, spec downstream.Spec) *downstream.ProbeResult {
		mu.Lock()
		inFlight++
		entered++
		if inFlight > peak {
			peak = inFlight
		}
		if entered == total {
			close(allIn)
		}
		mu.Unlock()

		select {
		case <-allIn:
		case <-pctx.Done():
		}

		mu.Lock()
		inFlight--
		mu.Unlock()
		finished <- struct{}{}
		return &downstream.ProbeResult{OK: true, ToolCount: 1}
	}
	// 有上限时这批探活凑不齐"全部到位"，靠试测超时收场；超时给短一点，
	// 用例不必干等 30 秒。
	svc.credTestTimeout = 300 * time.Millisecond

	out, err := svc.ImportCredentials(ctx, serverID, importSpecs(total))
	if err != nil {
		t.Fatalf("批量导入失败: %v", err)
	}
	if len(out) != total {
		t.Fatalf("导入返回 %d 条，期望 %d 条", len(out), total)
	}

	// 等这一批试测全部结束（每个探活结束报一次，不是靠时间猜）。
	for i := 0; i < total; i++ {
		select {
		case <-finished:
		case <-time.After(30 * time.Second):
			t.Fatalf("等待第 %d 个探活结束超时", i+1)
		}
	}

	mu.Lock()
	gotPeak, gotEntered := peak, entered
	mu.Unlock()

	if gotEntered != total {
		t.Errorf("实际发起 %d 次试测，期望 %d 次（每把密钥都要试一次）", gotEntered, total)
	}
	if gotPeak > credentialTestConcurrency {
		t.Errorf("同时在途的试测峰值 %d 超过上限 %d：批量导入不该对下游形成突发流量",
			gotPeak, credentialTestConcurrency)
	}
	if gotPeak < 2 {
		t.Errorf("同时在途的试测峰值只有 %d，用例根本没测出并发", gotPeak)
	}

	// 导入本身不该被试测的成败影响：库里与密钥池都要是完整的这一批。
	list, err := st.Credentials.List(ctx, serverID)
	if err != nil {
		t.Fatalf("读取密钥列表失败: %v", err)
	}
	if len(list) != total {
		t.Errorf("库里 %d 把密钥，期望 %d 把", len(list), total)
	}
	ring := svc.pool.KeyRing(serverID)
	if ring == nil {
		t.Fatalf("密钥池没有装载")
	}
	if ring.Len() != total {
		t.Errorf("密钥池 %d 把，期望 %d 把", ring.Len(), total)
	}
}

// 试测失败只影响那一条：整批导入不报错，成功的那几把照样写进去。
func TestImportCredentialsProbeFailureKeepsBatchIntact(t *testing.T) {
	const (
		total   = 6
		failing = 2
	)

	svc, st, logs := newCredentialTestSvc(t)
	ctx := context.Background()
	serverID := seedRemoteServer(t, st, "failsrv")

	finished := make(chan struct{}, total)
	svc.probeFn = func(pctx context.Context, spec downstream.Spec) *downstream.ProbeResult {
		defer func() { finished <- struct{}{} }()
		// 头里带 bad 的那把探不通，其余通过。
		if strings.Contains(spec.Headers["Authorization"], "bad") {
			return &downstream.ProbeResult{
				OK:     false,
				Kind:   downstream.KindAuthRequired,
				Stage:  downstream.StageListTools,
				Detail: "401 unauthorized",
			}
		}
		return &downstream.ProbeResult{OK: true, ToolCount: 2}
	}

	specs := importSpecs(total)
	for i := 0; i < failing; i++ {
		specs[i].Headers["Authorization"] = fmt.Sprintf("Bearer bad-token-%02d", i)
	}

	out, err := svc.ImportCredentials(ctx, serverID, specs)
	if err != nil {
		t.Fatalf("试测失败不该让整批导入报错: %v", err)
	}
	if len(out) != total {
		t.Fatalf("导入返回 %d 条，期望 %d 条", len(out), total)
	}

	for i := 0; i < total; i++ {
		select {
		case <-finished:
		case <-time.After(30 * time.Second):
			t.Fatalf("等待第 %d 个探活结束超时", i+1)
		}
	}

	// 成功的与失败的一把都写进了库。
	list, err := st.Credentials.List(ctx, serverID)
	if err != nil {
		t.Fatalf("读取密钥列表失败: %v", err)
	}
	if len(list) != total {
		t.Errorf("库里 %d 把密钥，期望 %d 把（失败的也要在库里，它已经导入了）", len(list), total)
	}

	// 密钥池与库里一致，且试测失败没有把任何一把判成弃用——试测失败可能
	// 只是下游在抖，"禁用"的判决留给在线结算。
	ring := svc.pool.KeyRing(serverID)
	if ring == nil {
		t.Fatalf("密钥池没有装载")
	}
	if ring.Len() != total {
		t.Errorf("密钥池 %d 把，期望 %d 把", ring.Len(), total)
	}
	for _, stt := range ring.Status() {
		if stt.State != downstream.KeyActive {
			t.Errorf("密钥 %s 的状态是 %s，期望 active（试测失败不该改状态）", stt.ID, stt.State)
		}
	}

	// 失败条目要有明确处理：逐条记一条 warn（跳过并报告）。
	if got := logs.FilterMessage("新密钥试测未通过（已照常入池，可在管理界面复核）").Len(); got != failing {
		t.Errorf("失败试测的 warn 日志 %d 条，期望 %d 条", got, failing)
	}
	if got := logs.FilterMessage("新密钥试测通过").Len(); got != total-failing {
		t.Errorf("成功试测的 info 日志 %d 条，期望 %d 条", got, total-failing)
	}
}

// 单条新增的语义没被改坏：只试测一次，只有一把进池。
func TestAddCredentialProbesExactlyOnce(t *testing.T) {
	svc, st, _ := newCredentialTestSvc(t)
	ctx := context.Background()
	serverID := seedRemoteServer(t, st, "onesrv")

	finished := make(chan struct{}, 4)
	svc.probeFn = func(pctx context.Context, spec downstream.Spec) *downstream.ProbeResult {
		finished <- struct{}{}
		return &downstream.ProbeResult{OK: true, ToolCount: 1}
	}

	c, err := svc.AddCredential(ctx, serverID, CredentialSpec{
		Label:   "solo",
		Headers: map[string]string{"Authorization": "Bearer solo"},
	})
	if err != nil {
		t.Fatalf("新增密钥失败: %v", err)
	}
	if c.ID == "" {
		t.Fatalf("新增密钥没有返回 id")
	}

	select {
	case <-finished:
	case <-time.After(30 * time.Second):
		t.Fatalf("单条新增没有触发试测")
	}
	// 只该试一次：多出来的探活说明单条路径也被批量逻辑带偏了。
	select {
	case <-finished:
		t.Errorf("单条新增触发了不止一次试测")
	case <-time.After(200 * time.Millisecond):
	}

	list, err := st.Credentials.List(ctx, serverID)
	if err != nil {
		t.Fatalf("读取密钥列表失败: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("库里 %d 把密钥，期望 1 把", len(list))
	}
	ring := svc.pool.KeyRing(serverID)
	if ring == nil || ring.Len() != 1 {
		t.Errorf("密钥池应有 1 把密钥，实际 %v", ring)
	}
}

// --- 同值检测与写入之间的竞态 ---

// dupBarrier 是一个"两边都到齐才放行"的同步点。
//
// 它存在的理由：同值检测是"先读库判断、再写库"两步，中间没有互斥，
// 两个并发的同值写入会双双通过检测。用 sleep 制造交错是靠不住的，
// 必须有一个能同时卡住两个调用的同步点——credCheckedHook 正是写
// 路径上"检测已过、尚未落库"的那个位置。
//
// 到达人数不足时按超时放行：修好之后第二个调用被写锁挡在临界区外，
// 根本到不了这里，等它等到天亮不如到点就走（用例照样能断言结果）。
type dupBarrier struct {
	mu      sync.Mutex
	arrived int
	want    int
	gate    chan struct{}
	wait    time.Duration
}

// newDupBarrier 造一个"want 个调用到齐才放行"的同步点。
func newDupBarrier(want int, wait time.Duration) *dupBarrier {
	return &dupBarrier{want: want, gate: make(chan struct{}), wait: wait}
}

// arrive 报告一个调用到达同步点，等齐（或超时）后返回。
func (b *dupBarrier) arrive() {
	b.mu.Lock()
	b.arrived++
	if b.arrived >= b.want {
		select {
		case <-b.gate:
		default:
			close(b.gate)
		}
	}
	b.mu.Unlock()

	timer := time.NewTimer(b.wait)
	defer timer.Stop()
	select {
	case <-b.gate:
	case <-timer.C:
	}
}

// 并发新增同一份凭据：只能有一个成功，另一个必须被判为重复。
//
// 修好之前：两个调用都在"检测已过、尚未落库"的位置被同步点卡住，
// 一起落库 → 库里两份同值凭据 → 用例失败。
// 修好之后：写路径进了临界区，第二个调用根本进不来，它会看到第一个
// 刚写进去的那份并被拒（ErrDuplicateCredential）。
func TestAddCredentialConcurrentSameHeadersRejected(t *testing.T) {
	svc, st, _ := newCredentialTestSvc(t)
	ctx := context.Background()
	serverID := seedRemoteServer(t, st, "dupsrv")

	svc.probeFn = func(pctx context.Context, spec downstream.Spec) *downstream.ProbeResult {
		return &downstream.ProbeResult{OK: true, ToolCount: 1}
	}

	barrier := newDupBarrier(2, 300*time.Millisecond)
	svc.credCheckedHook = barrier.arrive

	// 两个调用的 label 必须不同：label 有唯一索引兜底（那把 label 冲突
	// 会先炸出来，掩盖住同值检测这条路径）。这里要验的是"请求头完全
	// 相同"这一条——同一个上游身份配两把密钥。
	labels := []string{"first", "second"}
	sameHeaders := map[string]string{"Authorization": "Bearer same-token"}

	start := make(chan struct{})
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func(i int) {
			<-start
			_, err := svc.AddCredential(ctx, serverID, CredentialSpec{
				Label:   labels[i],
				Headers: sameHeaders,
			})
			results <- err
		}(i)
	}
	close(start)

	var ok, dup int
	for i := 0; i < 2; i++ {
		err := <-results
		switch {
		case err == nil:
			ok++
		case errors.Is(err, ErrDuplicateCredential):
			dup++
		default:
			t.Fatalf("并发新增返回了非预期的错误: %v", err)
		}
	}
	if ok != 1 || dup != 1 {
		t.Errorf("成功 %d 次、被判重复 %d 次，期望各 1 次", ok, dup)
	}

	list, err := st.Credentials.List(ctx, serverID)
	if err != nil {
		t.Fatalf("读取密钥列表失败: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("库里 %d 把密钥，期望 1 把（同值凭据不该留下两份）", len(list))
	}
}

// 并发批量导入同一份凭据：同样只能有一个成功。
//
// 批量导入是"同一个 Key 粘两遍"的高发场景，这条路径的同值检测是
// 内联实现（见 importCredentials），同样必须在临界区内。
func TestImportCredentialsConcurrentSameHeadersRejected(t *testing.T) {
	svc, st, _ := newCredentialTestSvc(t)
	ctx := context.Background()
	serverID := seedRemoteServer(t, st, "dupimport")

	svc.probeFn = func(pctx context.Context, spec downstream.Spec) *downstream.ProbeResult {
		return &downstream.ProbeResult{OK: true, ToolCount: 1}
	}

	barrier := newDupBarrier(2, 300*time.Millisecond)
	svc.credCheckedHook = barrier.arrive

	// label 不同、请求头相同（理由同上一个用例：label 冲突会先被唯一
	// 索引拦下，掩盖住同值检测这条路径）。
	sameHeaders := map[string]string{"Authorization": "Bearer same-token"}

	start := make(chan struct{})
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func(i int) {
			<-start
			_, err := svc.ImportCredentials(ctx, serverID, []CredentialSpec{{
				Label:   fmt.Sprintf("batch-%d", i),
				Headers: sameHeaders,
			}})
			results <- err
		}(i)
	}
	close(start)

	var ok, dup int
	for i := 0; i < 2; i++ {
		err := <-results
		switch {
		case err == nil:
			ok++
		case errors.Is(err, ErrDuplicateCredential):
			dup++
		default:
			t.Fatalf("并发导入返回了非预期的错误: %v", err)
		}
	}
	if ok != 1 || dup != 1 {
		t.Errorf("成功 %d 次、被判重复 %d 次，期望各 1 次", ok, dup)
	}

	list, err := st.Credentials.List(ctx, serverID)
	if err != nil {
		t.Fatalf("读取密钥列表失败: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("库里 %d 把密钥，期望 1 把（同值凭据不该留下两份）", len(list))
	}
}
