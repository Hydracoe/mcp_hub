package api_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/hydra/mcp-hub/internal/httpd"
)

// PUT 把 enabled 改成 false 时，连接必须一起退役。
//
// 编辑表单也能停用 server，这条路径与开关那条路径（POST /disable）必须
// 同语义：否则被编辑关掉的 server，它的常驻连接还留在池子里——既不释放，
// 也可能继续被用到。
//
// 判据是"池子里那条连接还在不在"：退役之后池里没有这条 server 的连接，
// 再取一次拿到的是新建的那条。
func TestUpdateServerDisableRetiresConnection(t *testing.T) {
	e := newEnv(t, httpd.Options{})
	ds := startDownstream(t, "x")
	ctx := context.Background()

	var created serverResp
	e.mustJSON(http.MethodPost, "/api/servers", map[string]any{
		"key": "srv", "transport": "streamable_http", "url": ds,
		"categoryId": e.mustCategory(),
	}, http.StatusCreated, &created)

	sv, err := e.store.Servers.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("读 server 失败: %v", err)
	}

	// 先让池子里有一条连接（真实建连，不打桩）。
	before, err := e.svc.Pool().Get(ctx, sv.Spec)
	if err != nil {
		t.Fatalf("建连失败: %v", err)
	}

	var updated serverResp
	e.mustJSON(http.MethodPut, "/api/servers/"+created.ID, map[string]any{
		"key": "srv", "transport": "streamable_http", "url": ds, "enabled": false,
	}, http.StatusOK, &updated)
	if updated.Enabled {
		t.Errorf("回显的 enabled = true, 期望 false")
	}

	after, err := e.svc.Pool().Get(ctx, sv.Spec)
	if err != nil {
		t.Fatalf("建连失败: %v", err)
	}
	if after == before {
		t.Errorf("编辑停用后连接没有退役：池里仍是同一条连接")
	}
}
