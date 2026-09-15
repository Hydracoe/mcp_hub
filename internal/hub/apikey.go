package hub

import (
	"context"
	"time"

	"github.com/hydra/mcp-hub/internal/store"
)

// callerAPIKeyKey 是"发起这次调用的接入 key id"在 ctx 中的键。
//
// 由 httpd 的鉴权中间件注入，聚合器的 callTool 读取后写进调用日志——
// 归因链是：哪个 AI 客户端（api key）→ 调了哪个下游工具。
type callerAPIKeyKey struct{}

// WithCallerAPIKey 标记一次请求的调用方。
func WithCallerAPIKey(ctx context.Context, keyID string) context.Context {
	if keyID == "" {
		return ctx
	}
	return context.WithValue(ctx, callerAPIKeyKey{}, keyID)
}

// CallerAPIKeyFrom 读出调用方 id，匿名调用返回空串。
func CallerAPIKeyFrom(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(callerAPIKeyKey{}).(string)
	return id
}

// APIKeysRequired 报告 /mcp 端点是否必须鉴权。
//
// 策略是"存在过接入 key 就强制，一把都没有则开放"：零 key 开放
// 保住了本地开发与现有部署的零配置体验；而创建 key 这个动作本身
// 就是启用鉴权的开关。按"存在"而非"启用"判断：全部停用应当是
// 把端点锁死，而不是向匿名敞开——停用是收紧，不是关闭鉴权。
func (s *Service) APIKeysRequired(ctx context.Context) (bool, error) {
	total, _, err := s.store.CountAPIKeys(ctx)
	if err != nil {
		return false, err
	}
	return total > 0, nil
}

// VerifyAPIKey 校验调用方出示的明文 key。
//
// 返回 key 信息与是否通过。停用的 key 一律拒绝——结果与"不存在"
// 相同，避免向调用方泄露这把 key 曾经存在。
func (s *Service) VerifyAPIKey(ctx context.Context, plaintext string) (store.APIKey, bool, error) {
	key, ok, err := s.store.LookupAPIKey(ctx, plaintext)
	if err != nil || !ok {
		return store.APIKey{}, false, err
	}
	s.touchAPIKey(key.ID)
	return key, true, nil
}

// touchInterval 控制 last_used_at 的落库频率。
//
// 每次调用都 UPDATE 一次等于把鉴权路径变成写热点，而用量时间戳
// 精确到分钟毫无意义——节流到每把 key 每分钟最多落库一次。
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
	s.store.TouchAPIKey(ctx, keyID, time.Now())
}
