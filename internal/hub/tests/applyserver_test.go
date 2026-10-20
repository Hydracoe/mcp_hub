package tests

import (
	"context"
	"testing"

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
