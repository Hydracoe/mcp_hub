package aggregate

import (
	"time"

	"github.com/hydra/mcp-hub/internal/downstream"
)

// LogRecord 是一次工具调用的事实记录。
type LogRecord struct {
	ServerID  string
	ServerKey string
	ToolName  string // 下游原始名
	Exposed   string
	Duration  time.Duration
	Status    string // ok | error | key
	Kind      downstream.ErrorKind
	Err       string

	// CredentialID 归因到具体密钥，单密钥模式为空。
	// 只记 id 不记明文：日志里落一份凭据，等于把密钥抄到了另一个地方。
	CredentialID string

	// Attempt 是这次调用用掉第几次密钥尝试（从 1 开始）。
	Attempt int

	// APIKeyID / APIKeyName 是发起调用的接入 key（外部 AI 客户端）及其
	// 调用当时的名字，匿名调用均为空；Args / Result 是请求现场（已截断），
	// 排障时"当时发了什么、拿回了什么"比任何统计都值钱。
	APIKeyID   string
	APIKeyName string
	Args       string
	Result     string
}

func (a *Aggregator) logCall(rec LogRecord) {
	if a.deps.Log != nil {
		a.deps.Log(rec)
	}
}

// adviceFor 给出下一步该做什么，是 AI 唯一能读到的"该怎么办"：
// 说错（例如让被限流的调用换工具）会让它放弃整条路径，而实际等几秒就恢复了。
func adviceFor(kind downstream.ErrorKind) string {
	switch kind {
	case downstream.KindNoUsableKey, downstream.KindRateLimited:
		return "这是上游的临时配额限制，稍后重试即可，不必改用其他工具。"
	case downstream.KindQuotaExhausted, downstream.KindAuthRequired:
		return "这是上游的凭据或额度问题，重试不会改善，需要在管理界面处理密钥。"
	case downstream.KindDraining:
		return "该上游正在停用收尾，待其完成后重试，或改用其他 server 的同类工具。"
	case downstream.KindUnknown:
		return "可稍后重试；若持续失败，请改用其他工具。"
	default:
		return "这是连接类问题，稍后重试即可。"
	}
}
