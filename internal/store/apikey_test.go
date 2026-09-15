package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// 接入 API key 的增删查与校验语义。
func TestAPIKeyCRUD(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// 空名拒绝
	if _, _, err := s.CreateAPIKey(ctx, "  "); !errors.Is(err, ErrAPIKeyNameRequired) {
		t.Fatalf("空名应报 ErrAPIKeyNameRequired，got %v", err)
	}

	// 创建：明文只在返回值里出现一次，库里只有 hash
	key, plaintext, err := s.CreateAPIKey(ctx, "claude-desktop")
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	if !strings.HasPrefix(plaintext, KeyPrefix) || len(plaintext) < 20 {
		t.Fatalf("明文格式不对: %q", plaintext)
	}
	if key.Prefix != plaintext[:10] {
		t.Fatalf("prefix 应为明文前 10 字节: %q vs %q", key.Prefix, plaintext[:10])
	}
	if key.ID == "" || !key.Enabled {
		t.Fatalf("创建结果不完整: %+v", key)
	}

	// 明文查找：对的通过，错的拒绝，停用的拒绝
	got, ok, err := s.LookupAPIKey(ctx, plaintext)
	if err != nil || !ok || got.ID != key.ID {
		t.Fatalf("明文查找失败: ok=%v err=%v", ok, err)
	}
	if _, ok, _ := s.LookupAPIKey(ctx, "mh-not-the-key"); ok {
		t.Fatal("错误明文不应通过")
	}
	if err := s.SetAPIKeyEnabled(ctx, key.ID, false); err != nil {
		t.Fatalf("停用失败: %v", err)
	}
	if _, ok, _ := s.LookupAPIKey(ctx, plaintext); ok {
		t.Fatal("停用后明文查找不应通过")
	}

	// 列表
	if err := s.SetAPIKeyEnabled(ctx, key.ID, true); err != nil {
		t.Fatalf("重新启用失败: %v", err)
	}
	keys, err := s.ListAPIKeys(ctx)
	if err != nil || len(keys) != 1 || keys[0].ID != key.ID {
		t.Fatalf("列表不符: %v %+v", err, keys)
	}

	// 删除后查不到，计数归零
	if err := s.DeleteAPIKey(ctx, key.ID); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if err := s.DeleteAPIKey(ctx, key.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("重复删除应 ErrNotFound，got %v", err)
	}
	total, _, err := s.CountAPIKeys(ctx)
	if err != nil || total != 0 {
		t.Fatalf("删除后计数应为 0: %d %v", total, err)
	}

	// last_used_at 更新可忽略失败，但不该 panic
	s.TouchAPIKey(ctx, key.ID, time.Now())
}
