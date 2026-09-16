package tests

import (
	"context"
	"testing"
	"time"

	"github.com/hydra/mcp-hub/internal/downstream"
	"github.com/hydra/mcp-hub/internal/store/logs"
)

func newLogs(t *testing.T) *logs.Store {
	t.Helper()
	return newTestStore(t).Logs
}

func TestCallLogs(t *testing.T) {
	ctx := context.Background()
	s := newLogs(t)

	for i := range 3 {
		err := s.Append(ctx, logs.LogEntry{
			ServerKey:   "fake",
			ToolName:    "alpha",
			ExposedName: "fake__alpha",
			DurationMS:  int64(i),
			Status:      "ok",
		})
		if err != nil {
			t.Fatalf("Append 失败: %v", err)
		}
		time.Sleep(2 * time.Millisecond) // 保证 ts 可比
	}

	got, total, err := s.List(ctx, logs.LogQuery{Limit: 10})
	if err != nil {
		t.Fatalf("List 失败: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("日志数 = %d, want 3", len(got))
	}
	if total != 3 {
		t.Errorf("总条数 = %d, want 3", total)
	}
	if got[0].TS.Before(got[2].TS) {
		t.Error("日志应按时间倒序")
	}

	n, err := s.Clear(ctx)
	if err != nil {
		t.Fatalf("Clear 失败: %v", err)
	}
	if n != 3 {
		t.Errorf("删除条数 = %d, want 3", n)
	}
}

// 日志筛选与分页。
func TestLogQueryFiltersAndPaginates(t *testing.T) {
	ctx := context.Background()
	s := newLogs(t)

	base := time.Now().Add(-time.Hour).Truncate(time.Millisecond)
	entries := []logs.LogEntry{
		{ServerID: "s1", ServerKey: "one", ToolName: "read_doc", ExposedName: "one__a", Status: "ok",
			DurationMS: 3, TS: base, CredentialID: "cred_alpha",
			APIKeyID: "k1", APIKeyName: "管理后台"},
		{ServerID: "s1", ServerKey: "one", ExposedName: "one__b", Status: "error",
			ErrorKind: downstream.KindTimeout, DurationMS: 5000, TS: base.Add(time.Minute),
			APIKeyID: "k1", APIKeyName: "管理后台"},
		// 第三条匿名：未创建任何接入 key 时的调用就是这样。
		{ServerID: "s2", ServerKey: "two", ExposedName: "two__c", Status: "ok", DurationMS: 7, TS: base.Add(2 * time.Minute)},
	}
	for _, e := range entries {
		if err := s.Append(ctx, e); err != nil {
			t.Fatalf("Append 失败: %v", err)
		}
	}

	// 倒序：最新的一条在最前。
	got, total, err := s.List(ctx, logs.LogQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 || len(got) != 3 {
		t.Fatalf("total=%d len=%d, want 3/3", total, len(got))
	}
	if got[0].ExposedName != "two__c" {
		t.Errorf("首条 = %q, want two__c（按时间倒序）", got[0].ExposedName)
	}

	// 按 server 过滤。total 反映的是筛选后的总数，不是全表。
	if _, total, _ = s.List(ctx, logs.LogQuery{ServerID: "s1"}); total != 2 {
		t.Errorf("按 server 过滤 total = %d, want 2", total)
	}

	// 按状态过滤。
	got, total, _ = s.List(ctx, logs.LogQuery{Status: "error"})
	if total != 1 || got[0].ExposedName != "one__b" {
		t.Errorf("按状态过滤得到 total=%d entry=%v", total, got)
	}

	// 按错误分类过滤。
	if _, total, _ = s.List(ctx, logs.LogQuery{Kind: downstream.KindTimeout}); total != 1 {
		t.Errorf("按分类过滤 total = %d, want 1", total)
	}

	// 按调用方过滤：日志要能回答"某个 AI 客户端都调了什么"。
	if _, total, _ = s.List(ctx, logs.LogQuery{APIKeyID: "k1"}); total != 2 {
		t.Errorf("按调用方过滤 total = %d, want 2", total)
	}

	// 匿名走保留值。空串在这里表示"不筛"，所以"只看匿名"必须另有词
	// 来表达 —— 否则它会被理解成"看全部"，筛出来的东西恰好相反。
	got, total, _ = s.List(ctx, logs.LogQuery{APIKeyID: logs.LogCallerAnonymous})
	if total != 1 || len(got) != 1 || got[0].ExposedName != "two__c" {
		t.Errorf("筛匿名得到 total=%d logs=%v, want 1/two__c", total, got)
	}

	// 调用方名字随行落库、原样读回（快照，不是查询时 join 出来的当前值）。
	got, _, _ = s.List(ctx, logs.LogQuery{APIKeyID: "k1"})
	if got[0].APIKeyName != "管理后台" {
		t.Errorf("调用方名字快照 = %q, want 管理后台", got[0].APIKeyName)
	}
	if got[0].APIKeyID != "k1" {
		t.Errorf("调用方 id = %q, want k1", got[0].APIKeyID)
	}

	// 自由文本搜索：覆盖"谁调的 / 调的谁 / 哪个工具 / 哪把下游密钥"。
	// 每条都断言条数而不是"有没有结果"—— 一个被忽略的搜索条件会恒返回
	// 全量，只测"非空"的话它照样绿。
	for _, c := range []struct {
		q    string
		want int
		why  string
	}{
		{"read_doc", 1, "按下游工具原名"},
		{"ONE__A", 1, "按暴露名，且不区分大小写"},
		{"one", 2, "按 server key"},
		{"管理", 2, "按调用方名字快照"},
		{"cred_alpha", 1, "按下游密钥 id"},
		{"no-such-thing", 0, "查无此词"},
		{"   ", 3, "全空白等于不筛"},
		{"%", 0, "通配符必须当字面量，否则等于匹配全部"},
	} {
		if _, total, _ = s.List(ctx, logs.LogQuery{Q: c.q}); total != c.want {
			t.Errorf("搜索 %q（%s）total = %d, want %d", c.q, c.why, total, c.want)
		}
	}

	// 分页：总数不变，本页只取 limit 条。
	got, total, _ = s.List(ctx, logs.LogQuery{Limit: 2})
	if len(got) != 2 || total != 3 {
		t.Errorf("分页得到 len=%d total=%d, want 2/3", len(got), total)
	}
	got, _, _ = s.List(ctx, logs.LogQuery{Limit: 2, Offset: 2})
	if len(got) != 1 || got[0].ExposedName != "one__a" {
		t.Errorf("第二页 = %v, want [one__a]", got)
	}

	// 时间窗过滤。
	if _, total, _ = s.List(ctx, logs.LogQuery{Since: base.Add(30 * time.Second)}); total != 2 {
		t.Errorf("按起始时间过滤 total = %d, want 2", total)
	}

	// ── 排序 ──

	// 正序：最旧的一条在最前。
	got, _, _ = s.List(ctx, logs.LogQuery{Order: logs.LogOrderAsc})
	if len(got) != 3 || got[0].ExposedName != "one__a" {
		t.Errorf("时间正序首条 = %v, want one__a", got)
	}

	// 按耗时倒序：5000ms 的那条排最前。
	got, _, _ = s.List(ctx, logs.LogQuery{Sort: logs.LogSortDuration, Order: logs.LogOrderDesc})
	if got[0].ExposedName != "one__b" {
		t.Errorf("耗时倒序首条 = %q, want one__b", got[0].ExposedName)
	}

	// 按耗时正序：3ms 的那条排最前。
	got, _, _ = s.List(ctx, logs.LogQuery{Sort: logs.LogSortDuration, Order: logs.LogOrderAsc})
	if got[0].ExposedName != "one__a" {
		t.Errorf("耗时正序首条 = %q, want one__a", got[0].ExposedName)
	}

	// 排序列白名单：乱填的 sort 不能变成 ORDER BY 注入，静默回退到时间倒序。
	got, _, _ = s.List(ctx, logs.LogQuery{Sort: logs.LogSort("ts; DROP TABLE call_logs")})
	if len(got) != 3 || got[0].ExposedName != "two__c" {
		t.Errorf("非法排序列应回退到时间倒序，得到 %v", got)
	}

	// 排序与分页组合：排序发生在取页之前，两页拼起来必须不重不漏。
	first, total, _ := s.List(ctx, logs.LogQuery{Sort: logs.LogSortDuration, Order: logs.LogOrderDesc, Limit: 2})
	second, _, _ := s.List(ctx, logs.LogQuery{Sort: logs.LogSortDuration, Order: logs.LogOrderDesc, Limit: 2, Offset: 2})
	page := []string{first[0].ExposedName, first[1].ExposedName, second[0].ExposedName}
	if total != 3 || page[0] != "one__b" || page[1] != "two__c" || page[2] != "one__a" {
		t.Errorf("排序 + 分页 = %v (total=%d), want [one__b two__c one__a]", page, total)
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
