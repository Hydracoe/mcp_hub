package tests

import (
	"context"
	"testing"

	"github.com/hydra/mcp-hub/internal/store/servers"
)

// 编辑表单把 server 关掉时，连接必须一起退役：两条停用路径（SetEnabled 与
// ApplyServer）对"停用"的定义必须一致，否则常驻连接（含 stdio 子进程）留在池里，
// 既不释放也可能继续被用到。判据是"池子里那条连接还在不在"。
func TestApplyServerDisableRetiresConnection(t *testing.T) {
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
	if err != nil {
		t.Fatalf("建 server 失败: %v", err)
	}
	sv, err := st.Servers.Get(ctx, res.ID)
	if err != nil {
		t.Fatalf("读 server 失败: %v", err)
	}

	// 先让池子里有一条连接（真实建连，不打桩）。
	before, err := svc.Pool().Get(ctx, sv.Spec)
	if err != nil {
		t.Fatalf("建连失败: %v", err)
	}

	// 编辑路径停用。
	if _, err := svc.ApplyServer(ctx, servers.Config{
		Spec:       sv.Spec,
		Enabled:    false,
		CategoryID: cat.ID,
	}); err != nil {
		t.Fatalf("编辑停用失败: %v", err)
	}

	after, err := svc.Pool().Get(ctx, sv.Spec)
	if err != nil {
		t.Fatalf("建连失败: %v", err)
	}
	if after == before {
		t.Errorf("编辑停用后连接没有退役：池里仍是同一条连接")
	}
}

// 停用之后再编辑一次（仍是停用）：不该重复退役。
// 退役是一次性的副作用：池里已没连接时再来一次只是噪音；误退役一个还在服务的
// 连接则会打断在飞调用。判据要看编辑前的状态，不能只看保存后的状态。
func TestApplyServerDisableTwiceDoesNotRetireAgain(t *testing.T) {
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
	if err != nil {
		t.Fatalf("建 server 失败: %v", err)
	}
	sv, err := st.Servers.Get(ctx, res.ID)
	if err != nil {
		t.Fatalf("读 server 失败: %v", err)
	}

	if _, err := svc.ApplyServer(ctx, servers.Config{
		Spec:       sv.Spec,
		Enabled:    false,
		CategoryID: cat.ID,
	}); err != nil {
		t.Fatalf("编辑停用失败: %v", err)
	}

	// 停用之后又有调用把它拉起来：池里重新有了一条连接。
	conn, err := svc.Pool().Get(ctx, sv.Spec)
	if err != nil {
		t.Fatalf("建连失败: %v", err)
	}

	// 再编辑一次，仍是停用。
	if _, err := svc.ApplyServer(ctx, servers.Config{
		Spec:       sv.Spec,
		Enabled:    false,
		CategoryID: cat.ID,
	}); err != nil {
		t.Fatalf("重复编辑失败: %v", err)
	}

	again, err := svc.Pool().Get(ctx, sv.Spec)
	if err != nil {
		t.Fatalf("建连失败: %v", err)
	}
	if again != conn {
		t.Errorf("重复编辑一个已停用的 server 不该再退役一次连接")
	}
}

// 无关的编辑（改个描述）不该拆掉正在服务的连接。
// 拆连接会打断在飞调用，只有真换了目标（url/command/env/超时/协议版本）才该重建。
func TestApplyServerUnrelatedEditKeepsConnection(t *testing.T) {
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
	if err != nil {
		t.Fatalf("建 server 失败: %v", err)
	}
	sv, err := st.Servers.Get(ctx, res.ID)
	if err != nil {
		t.Fatalf("读 server 失败: %v", err)
	}

	before, err := svc.Pool().Get(ctx, sv.Spec)
	if err != nil {
		t.Fatalf("建连失败: %v", err)
	}

	if _, err := svc.ApplyServer(ctx, servers.Config{
		Spec:        sv.Spec,
		Enabled:     true,
		Description: "改过的描述",
		CategoryID:  cat.ID,
	}); err != nil {
		t.Fatalf("编辑失败: %v", err)
	}

	after, err := svc.Pool().Get(ctx, sv.Spec)
	if err != nil {
		t.Fatalf("建连失败: %v", err)
	}
	if after != before {
		t.Errorf("无关的编辑不该退役正在服务的连接")
	}
}
