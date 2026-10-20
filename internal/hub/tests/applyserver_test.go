package tests

import (
	"context"
	"errors"
	"testing"

	"github.com/hydra/mcp-hub/internal/hub"
	"github.com/hydra/mcp-hub/internal/store/servers"
)

// 改 key 是更新，不是新建。
//
// 判据是"这条记录在不在"，不是"这个 key 在不在"：更新路径带着已知的
// id，改 key 只是同一行换了个前缀。按 key 判新建会把这次更新误判成
// 新建，于是走新建的分类校验——请求体里没带分类时直接被"新建 server
// 必须指定分类"挡住，改个 key 就改不动。
func TestApplyServerKeyChangeIsUpdateNotCreate(t *testing.T) {
	st, svc := openSvc(t)
	ctx := context.Background()

	cat, err := svc.CreateCategory(ctx, "天气", "weather", "天气查询")
	if err != nil {
		t.Fatalf("建分类失败: %v", err)
	}

	down := startDownstream(t, toolSpec{name: "forecast", desc: "查询城市天气"})
	res, err := svc.ApplyServer(ctx, servers.Config{
		Spec:       remoteSpec("wx", down),
		Enabled:    true,
		CategoryID: cat.ID,
	})
	if err != nil || !res.Created {
		t.Fatalf("建 server 失败: err=%v created=%v", err, res.Created)
	}

	sv, err := st.Servers.GetByKey(ctx, "wx")
	if err != nil {
		t.Fatalf("读 server 失败: %v", err)
	}

	// 改 key，且不带分类：嵌入未启用，新建路径必然拒掉这个请求。
	spec := sv.Spec
	spec.Key = "wx2"
	spec.Name = "wx2"
	upd, err := svc.ApplyServer(ctx, servers.Config{
		Spec:    spec,
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("改 key 应按更新处理: %v", err)
	}
	if upd.Created {
		t.Errorf("改 key 是更新，Created 应为 false")
	}
	if upd.ID != sv.Spec.ID {
		t.Errorf("改 key 应原地更新同一行: id=%s want %s", upd.ID, sv.Spec.ID)
	}

	// 新 key 能查到，老 key 查不到（还是那一条，不是多了一条）。
	sv2, err := st.Servers.GetByKey(ctx, "wx2")
	if err != nil {
		t.Fatalf("新 key 查不到: %v", err)
	}
	if sv2.Spec.ID != sv.Spec.ID {
		t.Errorf("新 key 应指向同一条记录: id=%s want %s", sv2.Spec.ID, sv.Spec.ID)
	}
	if _, err := st.Servers.GetByKey(ctx, "wx"); err == nil {
		t.Errorf("老 key 应已被换掉")
	}
}

// 同样的 key 再写一次（POST ?upsert=1 走的就是这条路径）不是新建：
// 它已有记录，不该再被新建的分类校验挡一次。
func TestApplyServerSameKeyIsNotCreate(t *testing.T) {
	_, svc := openSvc(t)
	ctx := context.Background()

	cat, err := svc.CreateCategory(ctx, "天气", "weather", "天气查询")
	if err != nil {
		t.Fatalf("建分类失败: %v", err)
	}

	down := startDownstream(t, toolSpec{name: "forecast", desc: "查询城市天气"})
	res, err := svc.ApplyServer(ctx, servers.Config{
		Spec:       remoteSpec("wx", down),
		Enabled:    true,
		CategoryID: cat.ID,
	})
	if err != nil || !res.Created {
		t.Fatalf("建 server 失败: err=%v created=%v", err, res.Created)
	}

	// id 为空 + key 已存在：覆盖写，不是新建。
	again, err := svc.ApplyServer(ctx, servers.Config{
		Spec:       remoteSpec("wx", down),
		Enabled:    true,
		CategoryID: cat.ID,
	})
	if err != nil {
		t.Fatalf("覆盖同一 key 失败: %v", err)
	}
	if again.Created {
		t.Errorf("覆盖已有 key 不是新建，Created 应为 false")
	}
	if again.ID != res.ID {
		t.Errorf("覆盖写应落在同一条记录: id=%s want %s", again.ID, res.ID)
	}
}

// 更新时给了不存在的分类必须被拦下。
//
// 分类只有"新建"那条路校验过存在性，更新这条路不校验：写进去的是一个
// 不存在的 id，也就是悬空引用——界面上那一列显示成空白分类，检索侧则
// 永远查不到它。校验要发生在落库之前：失败的写入不该把原记录改坏。
func TestApplyServerUpdateRejectsUnknownCategory(t *testing.T) {
	st, svc := openSvc(t)
	ctx := context.Background()

	cat, err := svc.CreateCategory(ctx, "天气", "weather", "天气查询")
	if err != nil {
		t.Fatalf("建分类失败: %v", err)
	}

	down := startDownstream(t, toolSpec{name: "forecast", desc: "查询城市天气"})
	res, err := svc.ApplyServer(ctx, servers.Config{
		Spec:       remoteSpec("wx", down),
		Enabled:    true,
		CategoryID: cat.ID,
	})
	if err != nil || !res.Created {
		t.Fatalf("建 server 失败: err=%v created=%v", err, res.Created)
	}

	spec := remoteSpec("wx", down)
	spec.ID = res.ID
	spec.Name = "改过的名字"
	_, err = svc.ApplyServer(ctx, servers.Config{
		Spec:       spec,
		Enabled:    true,
		CategoryID: "cat_ghost",
	})
	if err == nil {
		t.Fatalf("更新时给了不存在的分类应报错")
	}
	var missing *hub.CategoryNotFoundError
	if !errors.As(err, &missing) {
		t.Fatalf("错误类型 = %T, 期望 *hub.CategoryNotFoundError: %v", err, err)
	}

	// 原记录没被写坏：分类还在，名字也没被这次失败的写入改掉。
	sv, err := st.Servers.Get(ctx, res.ID)
	if err != nil {
		t.Fatalf("读 server 失败: %v", err)
	}
	if sv.CategoryID != cat.ID {
		t.Errorf("分类 = %q, 期望 %q（失败的写入不该改分类）", sv.CategoryID, cat.ID)
	}
	if sv.Spec.Name == "改过的名字" {
		t.Errorf("失败的写入不该把名字改掉")
	}
}

// 更新时没带分类：原分类要留着。
//
// "没提"不等于"清空"。更新路径以前把请求体里的空分类整体写回，于是
// 一次没带该字段的保存就把分类抹了（界面上那一列变空）。
func TestApplyServerUpdateKeepsCategoryWhenOmitted(t *testing.T) {
	st, svc := openSvc(t)
	ctx := context.Background()

	cat, err := svc.CreateCategory(ctx, "天气", "weather", "天气查询")
	if err != nil {
		t.Fatalf("建分类失败: %v", err)
	}

	down := startDownstream(t, toolSpec{name: "forecast", desc: "查询城市天气"})
	res, err := svc.ApplyServer(ctx, servers.Config{
		Spec:       remoteSpec("wx", down),
		Enabled:    true,
		CategoryID: cat.ID,
	})
	if err != nil || !res.Created {
		t.Fatalf("建 server 失败: err=%v created=%v", err, res.Created)
	}

	spec := remoteSpec("wx", down)
	spec.ID = res.ID
	upd, err := svc.ApplyServer(ctx, servers.Config{Spec: spec, Enabled: true})
	if err != nil {
		t.Fatalf("不带分类的更新失败: %v", err)
	}
	if upd.Created {
		t.Errorf("不带分类的更新仍是更新，Created 应为 false")
	}

	sv, err := st.Servers.Get(ctx, res.ID)
	if err != nil {
		t.Fatalf("读 server 失败: %v", err)
	}
	if sv.CategoryID != cat.ID {
		t.Errorf("分类 = %q, 期望 %q（没带分类不该抹掉原分类）", sv.CategoryID, cat.ID)
	}
}

// 覆盖写已有 key（POST ?upsert=1 走的就是这条路径）且没带分类时，
// 原分类同样要留着：这条路径没有"编辑前配置"可参考，只能按记录判。
func TestApplyServerSameKeyKeepsCategoryWhenOmitted(t *testing.T) {
	st, svc := openSvc(t)
	ctx := context.Background()

	cat, err := svc.CreateCategory(ctx, "天气", "weather", "天气查询")
	if err != nil {
		t.Fatalf("建分类失败: %v", err)
	}

	down := startDownstream(t, toolSpec{name: "forecast", desc: "查询城市天气"})
	if _, err := svc.ApplyServer(ctx, servers.Config{
		Spec:       remoteSpec("wx", down),
		Enabled:    true,
		CategoryID: cat.ID,
	}); err != nil {
		t.Fatalf("建 server 失败: %v", err)
	}

	// id 为空 + key 已存在 + 不带分类：覆盖写，不该动分类。
	if _, err := svc.ApplyServer(ctx, servers.Config{
		Spec:    remoteSpec("wx", down),
		Enabled: true,
	}); err != nil {
		t.Fatalf("覆盖同一 key 失败: %v", err)
	}

	sv, err := st.Servers.GetByKey(ctx, "wx")
	if err != nil {
		t.Fatalf("读 server 失败: %v", err)
	}
	if sv.CategoryID != cat.ID {
		t.Errorf("分类 = %q, 期望 %q（覆盖写没带分类不该抹掉原分类）", sv.CategoryID, cat.ID)
	}
}

// 明写"清空分类"才真的清空：退回未分类是显式动作，不是"忘了带"的
// 副作用。存量 server 允许未分类，所以清空本身是合法状态。
func TestApplyServerExplicitClearCategory(t *testing.T) {
	st, svc := openSvc(t)
	ctx := context.Background()

	cat, err := svc.CreateCategory(ctx, "天气", "weather", "天气查询")
	if err != nil {
		t.Fatalf("建分类失败: %v", err)
	}

	down := startDownstream(t, toolSpec{name: "forecast", desc: "查询城市天气"})
	res, err := svc.ApplyServer(ctx, servers.Config{
		Spec:       remoteSpec("wx", down),
		Enabled:    true,
		CategoryID: cat.ID,
	})
	if err != nil || !res.Created {
		t.Fatalf("建 server 失败: err=%v created=%v", err, res.Created)
	}

	spec := remoteSpec("wx", down)
	spec.ID = res.ID
	if _, err := svc.ApplyServer(ctx, servers.Config{
		Spec:          spec,
		Enabled:       true,
		ClearCategory: true,
	}); err != nil {
		t.Fatalf("清空分类失败: %v", err)
	}

	sv, err := st.Servers.Get(ctx, res.ID)
	if err != nil {
		t.Fatalf("读 server 失败: %v", err)
	}
	if sv.CategoryID != "" {
		t.Errorf("分类 = %q, 期望空（显式清空后应是未分类）", sv.CategoryID)
	}
}
