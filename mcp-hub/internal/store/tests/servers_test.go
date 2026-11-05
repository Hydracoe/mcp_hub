package tests

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hydra/mcp-hub/internal/downstream"
	"github.com/hydra/mcp-hub/internal/store/servers"
	"github.com/hydra/mcp-hub/internal/store/storeutil"
)

func newServers(t *testing.T) *servers.Store {
	t.Helper()
	return newTestStore(t).Servers
}

func TestServerCRUD(t *testing.T) {
	ctx := context.Background()
	s := newServers(t)

	spec := testSpec()
	if err := s.Upsert(ctx, servers.Config{Spec: spec, Enabled: true}); err != nil {
		t.Fatalf("Upsert 失败: %v", err)
	}

	got, err := s.GetByKey(ctx, "fake")
	if err != nil {
		t.Fatalf("GetByKey 失败: %v", err)
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
	if err := s.Upsert(ctx, servers.Config{Spec: spec, Enabled: true}); err != nil {
		t.Fatalf("更新 authMode 失败: %v", err)
	}
	got, err = s.GetByKey(ctx, "fake")
	if err != nil {
		t.Fatalf("回读失败: %v", err)
	}
	if got.Spec.AuthMode != downstream.AuthModeNone || got.Spec.AuthModeOr() != downstream.AuthModeNone {
		t.Errorf("AuthMode 未往返: %q / %q", got.Spec.AuthMode, got.Spec.AuthModeOr())
	}

	// 同 key 再写一次应更新而非新建。
	spec.Name = "Renamed"
	if err := s.Upsert(ctx, servers.Config{Spec: spec, Enabled: true}); err != nil {
		t.Fatalf("二次 Upsert 失败: %v", err)
	}
	list, err := s.List(ctx)
	if err != nil {
		t.Fatalf("List 失败: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("server 数 = %d, want 1", len(list))
	}
	if list[0].Spec.Name != "Renamed" {
		t.Errorf("Name = %q, want Renamed", list[0].Spec.Name)
	}

	if _, err := s.SetEnabledCascade(ctx, got.Spec.ID, false); err != nil {
		t.Fatalf("SetEnabledCascade 失败: %v", err)
	}
	after, _ := s.Get(ctx, got.Spec.ID)
	if after.Enabled {
		t.Error("应已禁用")
	}

	if _, err := s.Get(ctx, ""); !errors.Is(err, storeutil.ErrNotFound) {
		t.Errorf("不存在的 id 应返回 ErrNotFound，得到 %v", err)
	}
}

func TestSaveProbeWritesTools(t *testing.T) {
	ctx := context.Background()
	s := newServers(t)

	if err := s.Upsert(ctx, servers.Config{Spec: testSpec(), Enabled: true}); err != nil {
		t.Fatal(err)
	}
	srv, _ := s.GetByKey(ctx, "fake")

	if err := s.SaveProbe(ctx, srv.Spec.ID, okProbe()); err != nil {
		t.Fatalf("SaveProbe 失败: %v", err)
	}

	updated, err := s.Get(ctx, srv.Spec.ID)
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
	s := newServers(t)

	if err := s.Upsert(ctx, servers.Config{Spec: testSpec(), Enabled: true}); err != nil {
		t.Fatal(err)
	}
	srv, _ := s.GetByKey(ctx, "fake")
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

	updated, _ := s.Get(ctx, srv.Spec.ID)
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
	s := newServers(t)

	if err := s.Upsert(ctx, servers.Config{Spec: testSpec(), Enabled: true}); err != nil {
		t.Fatal(err)
	}
	srv, _ := s.GetByKey(ctx, "fake")
	if err := s.SaveProbe(ctx, srv.Spec.ID, okProbe()); err != nil {
		t.Fatal(err)
	}

	if err := s.Delete(ctx, srv.Spec.ID); err != nil {
		t.Fatalf("Delete 失败: %v", err)
	}
	tools, _ := s.ListTools(ctx)
	if len(tools) != 0 {
		t.Errorf("级联删除后工具数应为 0，实际 %d", len(tools))
	}
	if err := s.Delete(ctx, srv.Spec.ID); !errors.Is(err, storeutil.ErrNotFound) {
		t.Errorf("重复删除应返回 ErrNotFound，得到 %v", err)
	}
}

// 两个 server 抢同一个 key 时必须报可识别的错误，方便上层映射成 409。
func TestDuplicateKeyIsRejected(t *testing.T) {
	ctx := context.Background()
	s := newServers(t)

	if err := s.Upsert(ctx, servers.Config{Spec: testSpec(), Enabled: true}); err != nil {
		t.Fatal(err)
	}

	dup := testSpec()
	dup.ID = storeutil.NewID()
	if err := s.Upsert(ctx, servers.Config{Spec: dup, Enabled: true}); !errors.Is(err, servers.ErrKeyTaken) {
		t.Fatalf("错误 = %v, want ErrKeyTaken", err)
	}

	list, _ := s.List(ctx)
	if len(list) != 1 {
		t.Errorf("server 数 = %d, want 1（拒绝后不应留下第二条）", len(list))
	}
}
