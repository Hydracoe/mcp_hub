package hub

import (
	"context"
	"time"

	"github.com/hydra/mcp-hub/internal/store/apikeys"
)

// APIKeysRequired 报告 /mcp 端点是否必须鉴权。按"存在"而非"启用"判断：
// 只要存在过接入 key 就强制鉴权，全部停用时端点也应锁死，不得向匿名敞开。
func (s *Service) APIKeysRequired(ctx context.Context) (bool, error) {
	total, _, err := s.store.APIKeys.Count(ctx)
	if err != nil {
		return false, err
	}
	return total > 0, nil
}

// VerifyAPIKey 校验调用方出示的明文 key。
//
// 停用的 key 与不存在的 key 一律拒绝且响应一致，避免泄露其存在性。
func (s *Service) VerifyAPIKey(ctx context.Context, plaintext string) (apikeys.APIKey, bool, error) {
	key, ok, err := s.store.APIKeys.Lookup(ctx, plaintext)
	if err != nil || !ok {
		return apikeys.APIKey{}, false, err
	}
	s.touchAPIKey(key.ID)
	return key, true, nil
}

// touchInterval 控制 last_used_at 的落库频率：每把 key 每分钟最多一次。
// 每次调用都 UPDATE 会让鉴权路径变成写热点。
const touchInterval = time.Minute

func (s *Service) touchAPIKey(keyID string) {
	if keyID == "" {
		return
	}
	s.touchMu.Lock()
	if s.touchLast == nil {
		s.touchLast = map[string]time.Time{}
	}
	if at, ok := s.touchLast[keyID]; ok && time.Since(at) < touchInterval {
		s.touchMu.Unlock()
		return
	}
	s.touchLast[keyID] = time.Now()
	s.touchMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	s.store.APIKeys.Touch(ctx, keyID, time.Now())
}
