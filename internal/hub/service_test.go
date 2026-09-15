package hub_test

import (
	"context"
	"testing"

	"github.com/hydra/mcp-hub/internal/store"
)

// 停用的 server 不该出现在全量测试报告里：它既不是"健康"也不是"失败"，
// 它是一个用户主动做的决定。
//
// 回归守卫：早先的实现把停用项也填进 results，末尾统计靠 "ServerKey 非空"
// 判断该不该计数——而停用项的 ServerKey 早就被填好了，于是"停用"被
// 报成"探活失败"，Healthy + Failed 会大于 Total。
func TestRefreshAllExcludesDisabled(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()

	// 启用但连不上：这一条应该被报成失败。
	if err := e.store.UpsertServer(ctx, store.Config{
		Spec:    remoteSpec("broken", deadURL(t)),
		Enabled: true,
	}); err != nil {
		t.Fatalf("写入 broken 失败: %v", err)
	}

	// 停用：这一条不该进入报告，也不该计入成败。
	if err := e.store.UpsertServer(ctx, store.Config{
		Spec:    remoteSpec("stopped", deadURL(t)),
		Enabled: false,
	}); err != nil {
		t.Fatalf("写入 stopped 失败: %v", err)
	}

	report := e.svc.RefreshAll(ctx)

	if report.Total != 1 || report.Disabled != 1 {
		t.Errorf("Total=%d Disabled=%d, 期望 1/1", report.Total, report.Disabled)
	}
	if report.Healthy != 0 || report.Failed != 1 {
		t.Errorf("Healthy=%d Failed=%d, 期望 0/1", report.Healthy, report.Failed)
	}
	if got := report.Healthy + report.Failed; got != report.Total {
		t.Errorf("Healthy+Failed=%d 与 Total=%d 不一致", got, report.Total)
	}
	if len(report.Results) != 1 {
		t.Fatalf("Results 有 %d 条, 期望 1（不该包含停用项）", len(report.Results))
	}
	if report.Results[0].ServerKey != "broken" {
		t.Errorf("Results[0].ServerKey = %q, 期望 broken", report.Results[0].ServerKey)
	}
}

// 全部停用时报告仍应自洽，且不因没有可探活项而出问题。
func TestRefreshAllAllDisabled(t *testing.T) {
	e := newEnv(t, nil)

	if err := e.store.UpsertServer(context.Background(), store.Config{
		Spec:    stdioSpec("only", "/nonexistent/mcp-hub-test-binary"),
		Enabled: false,
	}); err != nil {
		t.Fatalf("写入失败: %v", err)
	}

	report := e.svc.RefreshAll(context.Background())

	if report.Total != 0 || report.Disabled != 1 {
		t.Errorf("Total=%d Disabled=%d, 期望 0/1", report.Total, report.Disabled)
	}
	if report.Healthy != 0 || report.Failed != 0 {
		t.Errorf("Healthy=%d Failed=%d, 期望 0/0", report.Healthy, report.Failed)
	}
	if len(report.Results) != 0 {
		t.Errorf("Results 有 %d 条, 期望 0", len(report.Results))
	}
}
