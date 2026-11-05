package api

import (
	"testing"
	"time"

	"github.com/hydra/mcp-hub/internal/store/apikeys"
)

// 接入 key 的时间字段契约：出 Unix 毫秒，未使用过时 lastUsedAt 为 0。
//
// 这两条都是给界面用的：毫秒与前端其余接口一致（不必为这一个接口
// 单独写解析），而 0 让界面能区分"从未使用"与"很久以前用过"——
// 把零值 time 直出成 RFC3339 的话，那会渲染成 0001-01-01。
func TestToAPIKeyItem(t *testing.T) {
	created := time.Date(2026, 9, 16, 10, 30, 0, 0, time.UTC)
	used := time.Date(2026, 9, 16, 11, 0, 0, 0, time.UTC)

	t.Run("时间出 Unix 毫秒", func(t *testing.T) {
		got := toAPIKeyItem(apikeys.APIKey{
			ID:         "k1",
			Name:       "CI 流水线",
			Prefix:     "mh-3f9c2a1",
			Enabled:    true,
			LastUsedAt: used,
			CreatedAt:  created,
		})

		if got.ID != "k1" || got.Name != "CI 流水线" || got.Prefix != "mh-3f9c2a1" || !got.Enabled {
			t.Errorf("基础字段没有原样带出: %+v", got)
		}
		if got.CreatedAt != created.UnixMilli() {
			t.Errorf("CreatedAt = %d, 期望 %d", got.CreatedAt, created.UnixMilli())
		}
		if got.LastUsedAt != used.UnixMilli() {
			t.Errorf("LastUsedAt = %d, 期望 %d", got.LastUsedAt, used.UnixMilli())
		}
	})

	t.Run("从未使用时 lastUsedAt 为 0", func(t *testing.T) {
		got := toAPIKeyItem(apikeys.APIKey{ID: "k2", CreatedAt: created})
		if got.LastUsedAt != 0 {
			t.Errorf("LastUsedAt = %d, 期望 0（零值时间不该落成 0001-01-01）", got.LastUsedAt)
		}
	})
}
