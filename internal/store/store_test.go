package store

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hydra/mcp-hub/internal/downstream"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func testSpec() downstream.Spec {
	return downstream.Spec{
		Key:       "fake",
		Name:      "Fake MCP",
		Transport: downstream.TransportStreamableHTTP,
		URL:       "http://127.0.0.1:1234/mcp",
		Headers:   map[string]string{"Authorization": "Bearer t"},
		Timeout:   5 * time.Second,
	}
}

func okProbe() *downstream.ProbeResult {
	return &downstream.ProbeResult{
		OK:              true,
		ProtocolVersion: "2026-07-28",
		ServerName:      "fake-mcp",
		ServerVersion:   "1.2.3",
		Capabilities:    []string{"tools"},
		ToolCount:       2,
		Tools: []downstream.ToolInfo{
			{Name: "alpha", Description: "first"},
			{Name: "beta", Description: "second"},
		},
		Latency:   downstream.StageLatency{Total: 42},
		CheckedAt: time.Now(),
	}
}

func TestServerCRUD(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	spec := testSpec()
	if err := s.UpsertServer(ctx, Config{Spec: spec, Enabled: true}); err != nil {
		t.Fatalf("UpsertServer 失败: %v", err)
	}

	got, err := s.GetServerByKey(ctx, "fake")
	if err != nil {
		t.Fatalf("GetServerByKey 失败: %v", err)
	}
	if got.Spec.URL != spec.URL {
		t.Errorf("URL = %q, want %q", got.Spec.URL, spec.URL)
	}
	if got.Spec.Headers["Authorization"] != "Bearer t" {
		t.Errorf("Headers 未往返: %v", got.Spec.Headers)
	}
	if got.Spec.AuthModeOr() != downstream.AuthModeHeader {
		t.Errorf("AuthMode 推断 = %q, want header（配了头但未声明）", got.Spec.AuthModeOr())
	}
	if !got.Enabled {
		t.Error("应当默认启用")
	}

	// 显式声明的 authMode 要原样往返；免鉴权 server 保持 none。
	spec.AuthMode = downstream.AuthModeNone
	spec.Headers = nil
	if err := s.UpsertServer(ctx, Config{Spec: spec, Enabled: true}); err != nil {
		t.Fatalf("更新 authMode 失败: %v", err)
	}
	got, err = s.GetServerByKey(ctx, "fake")
	if err != nil {
		t.Fatalf("回读失败: %v", err)
	}
	if got.Spec.AuthMode != downstream.AuthModeNone || got.Spec.AuthModeOr() != downstream.AuthModeNone {
		t.Errorf("AuthMode 未往返: %q / %q", got.Spec.AuthMode, got.Spec.AuthModeOr())
	}

	// 同 key 再写一次应更新而非新建。
	spec.Name = "Renamed"
	if err := s.UpsertServer(ctx, Config{Spec: spec, Enabled: true}); err != nil {
		t.Fatalf("二次 UpsertServer 失败: %v", err)
	}
	list, err := s.ListServers(ctx)
	if err != nil {
		t.Fatalf("ListServers 失败: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("server 数 = %d, want 1", len(list))
	}
	if list[0].Spec.Name != "Renamed" {
		t.Errorf("Name = %q, want Renamed", list[0].Spec.Name)
	}

	if err := s.SetEnabled(ctx, got.Spec.ID, false); err != nil {
		t.Fatalf("SetEnabled 失败: %v", err)
	}
	after, _ := s.GetServer(ctx, got.Spec.ID)
	if after.Enabled {
		t.Error("应已禁用")
	}

	if _, err := s.GetServer(ctx, ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("不存在的 id 应返回 ErrNotFound，得到 %v", err)
	}
}

func TestSaveProbeWritesTools(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if err := s.UpsertServer(ctx, Config{Spec: testSpec(), Enabled: true}); err != nil {
		t.Fatal(err)
	}
	srv, _ := s.GetServerByKey(ctx, "fake")

	if err := s.SaveProbe(ctx, srv.Spec.ID, okProbe()); err != nil {
		t.Fatalf("SaveProbe 失败: %v", err)
	}

	updated, err := s.GetServer(ctx, srv.Spec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != "healthy" {
		t.Errorf("Status = %q, want healthy", updated.Status)
	}
	if updated.ProtocolEra != "modern" {
		t.Errorf("ProtocolEra = %q, want modern", updated.ProtocolEra)
	}
	if updated.Spec.ProtocolVersion != "2026-07-28" {
		t.Errorf("协商结果未回写: %q", updated.Spec.ProtocolVersion)
	}
	if updated.LastLatencyMS != 42 {
		t.Errorf("LastLatencyMS = %d, want 42", updated.LastLatencyMS)
	}

	tools, err := s.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools 失败: %v", err)
	}
	if len(tools) != 2 {
		t.Fatalf("工具数 = %d, want 2", len(tools))
	}
	if tools[0].ExposedName != "fake__alpha" {
		t.Errorf("ExposedName = %q, want fake__alpha", tools[0].ExposedName)
	}
}

// 探活失败不得清空已有工具缓存——下游抖动不该让工具凭空消失。
func TestSaveProbeFailureKeepsTools(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if err := s.UpsertServer(ctx, Config{Spec: testSpec(), Enabled: true}); err != nil {
		t.Fatal(err)
	}
	srv, _ := s.GetServerByKey(ctx, "fake")
	if err := s.SaveProbe(ctx, srv.Spec.ID, okProbe()); err != nil {
		t.Fatal(err)
	}

	failed := &downstream.ProbeResult{
		OK:        false,
		Stage:     downstream.StageNetwork,
		Kind:      downstream.KindDNSFail,
		Detail:    "lookup failed",
		CheckedAt: time.Now(),
	}
	if err := s.SaveProbe(ctx, srv.Spec.ID, failed); err != nil {
		t.Fatalf("SaveProbe 失败: %v", err)
	}

	updated, _ := s.GetServer(ctx, srv.Spec.ID)
	if updated.Status != "offline" {
		t.Errorf("Status = %q, want offline", updated.Status)
	}
	if updated.LastErrorKind != downstream.KindDNSFail {
		t.Errorf("LastErrorKind = %q, want DNS_FAIL", updated.LastErrorKind)
	}

	tools, _ := s.ListTools(ctx)
	if len(tools) != 2 {
		t.Errorf("工具缓存应保留 2 项，实际 %d", len(tools))
	}
}

func TestDeleteServerCascades(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if err := s.UpsertServer(ctx, Config{Spec: testSpec(), Enabled: true}); err != nil {
		t.Fatal(err)
	}
	srv, _ := s.GetServerByKey(ctx, "fake")
	if err := s.SaveProbe(ctx, srv.Spec.ID, okProbe()); err != nil {
		t.Fatal(err)
	}

	if err := s.DeleteServer(ctx, srv.Spec.ID); err != nil {
		t.Fatalf("DeleteServer 失败: %v", err)
	}
	tools, _ := s.ListTools(ctx)
	if len(tools) != 0 {
		t.Errorf("级联删除后工具数应为 0，实际 %d", len(tools))
	}
	if err := s.DeleteServer(ctx, srv.Spec.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("重复删除应返回 ErrNotFound，得到 %v", err)
	}
}

func TestCallLogs(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	for i := range 3 {
		err := s.AppendLog(ctx, LogEntry{
			ServerKey:   "fake",
			ToolName:    "alpha",
			ExposedName: "fake__alpha",
			DurationMS:  int64(i),
			Status:      "ok",
		})
		if err != nil {
			t.Fatalf("AppendLog 失败: %v", err)
		}
		time.Sleep(2 * time.Millisecond) // 保证 ts 可比
	}

	logs, total, err := s.ListLogs(ctx, LogQuery{Limit: 10})
	if err != nil {
		t.Fatalf("ListLogs 失败: %v", err)
	}
	if len(logs) != 3 {
		t.Fatalf("日志数 = %d, want 3", len(logs))
	}
	if total != 3 {
		t.Errorf("总条数 = %d, want 3", total)
	}
	if logs[0].TS.Before(logs[2].TS) {
		t.Error("日志应按时间倒序")
	}

	n, err := s.ClearLogs(ctx)
	if err != nil {
		t.Fatalf("ClearLogs 失败: %v", err)
	}
	if n != 3 {
		t.Errorf("删除条数 = %d, want 3", n)
	}
}

// 工具过滤配置必须完整往返，否则界面里保存过的规则会静默失效。
func TestToolFilterRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if err := s.UpsertServer(ctx, Config{
		Spec:           testSpec(),
		Enabled:        true,
		ToolFilterMode: FilterBlock,
		ToolFilterList: []string{"beta"},
	}); err != nil {
		t.Fatalf("UpsertServer 失败: %v", err)
	}

	got, err := s.GetServerByKey(ctx, "fake")
	if err != nil {
		t.Fatal(err)
	}
	if got.ToolFilterMode != FilterBlock {
		t.Errorf("ToolFilterMode = %q, want block", got.ToolFilterMode)
	}
	if len(got.ToolFilterList) != 1 || got.ToolFilterList[0] != "beta" {
		t.Errorf("ToolFilterList = %v, want [beta]", got.ToolFilterList)
	}

	// 未显式配置时默认不过滤。
	if err := s.UpsertServer(ctx, Config{Spec: testSpec(), Enabled: true}); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetServerByKey(ctx, "fake")
	if got.ToolFilterMode != FilterAllowAll {
		t.Errorf("默认模式 = %q, want allow_all", got.ToolFilterMode)
	}
}

// allow 模式配空名单会静默清空工具，症状是"AI 的工具凭空消失"，
// 极难定位，因此必须在入口就拒绝。
func TestToolFilterRejectsEmptyAllowList(t *testing.T) {
	s := newTestStore(t)

	err := s.UpsertServer(context.Background(), Config{
		Spec:           testSpec(),
		Enabled:        true,
		ToolFilterMode: FilterAllow,
	})
	if err == nil {
		t.Fatal("allow 模式配空名单应当被拒绝")
	}
	if !strings.Contains(err.Error(), "名单") {
		t.Errorf("错误信息应说明原因，得到: %v", err)
	}
}

// 两个 server 抢同一个 key 时必须报可识别的错误，方便上层映射成 409。
func TestDuplicateKeyIsRejected(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if err := s.UpsertServer(ctx, Config{Spec: testSpec(), Enabled: true}); err != nil {
		t.Fatal(err)
	}

	dup := testSpec()
	dup.ID = NewID()
	if err := s.UpsertServer(ctx, Config{Spec: dup, Enabled: true}); !errors.Is(err, ErrKeyTaken) {
		t.Fatalf("错误 = %v, want ErrKeyTaken", err)
	}

	list, _ := s.ListServers(ctx)
	if len(list) != 1 {
		t.Errorf("server 数 = %d, want 1（拒绝后不应留下第二条）", len(list))
	}
}

// 日志筛选与分页。
func TestLogQueryFiltersAndPaginates(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	base := time.Now().Add(-time.Hour).Truncate(time.Millisecond)
	entries := []LogEntry{
		{ServerID: "s1", ServerKey: "one", ExposedName: "one__a", Status: "ok", DurationMS: 3, TS: base},
		{ServerID: "s1", ServerKey: "one", ExposedName: "one__b", Status: "error",
			ErrorKind: downstream.KindTimeout, DurationMS: 5000, TS: base.Add(time.Minute)},
		{ServerID: "s2", ServerKey: "two", ExposedName: "two__c", Status: "ok", DurationMS: 7, TS: base.Add(2 * time.Minute)},
	}
	for _, e := range entries {
		if err := s.AppendLog(ctx, e); err != nil {
			t.Fatalf("AppendLog 失败: %v", err)
		}
	}

	// 倒序：最新的一条在最前。
	logs, total, err := s.ListLogs(ctx, LogQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 || len(logs) != 3 {
		t.Fatalf("total=%d len=%d, want 3/3", total, len(logs))
	}
	if logs[0].ExposedName != "two__c" {
		t.Errorf("首条 = %q, want two__c（按时间倒序）", logs[0].ExposedName)
	}

	// 按 server 过滤。total 反映的是筛选后的总数，不是全表。
	if _, total, _ = s.ListLogs(ctx, LogQuery{ServerID: "s1"}); total != 2 {
		t.Errorf("按 server 过滤 total = %d, want 2", total)
	}

	// 按状态过滤。
	logs, total, _ = s.ListLogs(ctx, LogQuery{Status: "error"})
	if total != 1 || logs[0].ExposedName != "one__b" {
		t.Errorf("按状态过滤得到 total=%d entry=%v", total, logs)
	}

	// 按错误分类过滤。
	if _, total, _ = s.ListLogs(ctx, LogQuery{Kind: downstream.KindTimeout}); total != 1 {
		t.Errorf("按分类过滤 total = %d, want 1", total)
	}

	// 分页：总数不变，本页只取 limit 条。
	logs, total, _ = s.ListLogs(ctx, LogQuery{Limit: 2})
	if len(logs) != 2 || total != 3 {
		t.Errorf("分页得到 len=%d total=%d, want 2/3", len(logs), total)
	}
	logs, _, _ = s.ListLogs(ctx, LogQuery{Limit: 2, Offset: 2})
	if len(logs) != 1 || logs[0].ExposedName != "one__a" {
		t.Errorf("第二页 = %v, want [one__a]", logs)
	}

	// 时间窗过滤。
	if _, total, _ = s.ListLogs(ctx, LogQuery{Since: base.Add(30 * time.Second)}); total != 2 {
		t.Errorf("按起始时间过滤 total = %d, want 2", total)
	}

	st, err := s.Stats(ctx, base.Add(-time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if st.Total != 3 || st.Failed != 1 {
		t.Errorf("统计 total=%d failed=%d, want 3/1", st.Total, st.Failed)
	}
	if st.MaxMS != 5000 {
		t.Errorf("最长耗时 = %d, want 5000", st.MaxMS)
	}
	if st.AvgMS < 1600 || st.AvgMS > 1700 {
		t.Errorf("平均耗时 = %f, want ≈1670", st.AvgMS)
	}
}
