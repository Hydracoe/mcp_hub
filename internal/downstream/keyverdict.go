package downstream

import (
	"net/http"
	"time"
)

// KeyState 是一把密钥的运行状态。
//
// 只有三种，且恢复方式各不相同：cooling 到点自动恢复，disabled 只有人工能救。
// 把两者合成一个"不可用"是这类系统最常见的语义丢失——用户会以为等着就好。
type KeyState string

const (
	KeyActive   KeyState = "active"
	KeyCooling  KeyState = "cooling"
	KeyDisabled KeyState = "disabled"
)

// KeyVerdict 是针对密钥调度作出的判决。
//
// 与 ErrorKind 并列而非合并：ErrorKind 回答"下游为什么失败"，面向人；
// KeyVerdict 回答"这把密钥还能不能用"，面向调度器。同一次观测可以给出两个
// 不同结论——5xx 对前者是 UPSTREAM_ERROR，对后者是"与密钥无关，别动它"。
// 合并成一个枚举，任何一侧加值都会污染另一侧。
type KeyVerdict string

const (
	// VerdictOK 表示调用成功。它不是"中性失败"，而是唯一能重置连续限流计数
	// 的信号——冷却到期却不重置，退避就永远升不上去。
	VerdictOK KeyVerdict = "OK"

	// VerdictNeutral 表示失败与密钥无关（5xx、超时、网络、业务错误）。
	VerdictNeutral KeyVerdict = "NEUTRAL"

	VerdictRateLimited    KeyVerdict = "RATE_LIMITED"
	VerdictQuotaExhausted KeyVerdict = "QUOTA_EXHAUSTED"
	VerdictInvalidKey     KeyVerdict = "INVALID_KEY"
	VerdictKeyRevoked     KeyVerdict = "KEY_REVOKED"
)

// VerdictOf 由错误分类与观测推导密钥判决。
//
// 401 与 403 在这里分道扬镳：401 语义无歧义（凭据不对），403 不是——它也可能是
// IP 白名单、scope 不足、地区封锁，换密钥同样没用。因此 403 不直接判定为
// "该弃用"，由结算环节给两次机会。
func VerdictOf(kind ErrorKind, obs Observation) KeyVerdict {
	switch kind {
	case KindRateLimited:
		return VerdictRateLimited
	case KindQuotaExhausted:
		return VerdictQuotaExhausted
	case KindAuthRequired:
		if obs.StatusCode == http.StatusUnauthorized {
			return VerdictInvalidKey
		}
		return VerdictKeyRevoked
	case KindNone:
		return VerdictOK
	}
	return VerdictNeutral
}

// Retryable 报告该判决是否允许"换一把密钥再试一次"。
//
// 只有确定未被执行的四类信号在此列：401 / 402 / 403 / 429。超时、5xx 与业务
// 错误都可能**已经执行过**——密钥池让重试变便宜了，但"可能重复下单"没有。
func (v KeyVerdict) Retryable() bool {
	switch v {
	case VerdictRateLimited, VerdictQuotaExhausted, VerdictInvalidKey, VerdictKeyRevoked:
		return true
	}
	return false
}

// Disables 报告该判决是否指向"最终弃用"。
func (v KeyVerdict) Disables() bool {
	switch v {
	case VerdictQuotaExhausted, VerdictInvalidKey, VerdictKeyRevoked:
		return true
	}
	return false
}

// Human 返回面向用户的中文解释。
func (v KeyVerdict) Human() string {
	switch v {
	case VerdictOK:
		return "调用成功"
	case VerdictNeutral:
		return "失败与密钥无关"
	case VerdictRateLimited:
		return "被限流：冷却后自动恢复"
	case VerdictQuotaExhausted:
		return "额度或余额耗尽：需要更换或充值"
	case VerdictInvalidKey:
		return "凭据无效：已弃用"
	case VerdictKeyRevoked:
		return "凭据被吊销或封禁"
	}
	return string(v)
}

// 退避与判定的默认参数。
const (
	// DefaultCoolingBase 是指数退避的基数：1s、2s、4s……
	DefaultCoolingBase = time.Second

	// DefaultCoolingCap 是冷却上限 5 分钟。RPM 的窗口本来就是分钟级，
	// 退避到这个量级再往上涨没有意义；真需要等更久的，应归类为
	// QUOTA_EXHAUSTED 走弃用——"等 5 分钟"和"这把没钱了"是两种处境。
	DefaultCoolingCap = 5 * time.Minute

	// DefaultRevokedThreshold 是 403 连续多少次后弃用。
	DefaultRevokedThreshold = 2

	// DefaultLimitedDisableThreshold 是连续限流多少次后弃用（R16）。
	// 退避 1s 起步：第 10 次时 1<<9=512s 已越过 5 分钟上限，再往后只会在
	// "5 分钟一轮"的循环里原地打转——那已经是账号级限流的形状，该交给人了。
	DefaultLimitedDisableThreshold = 10

	// collectiveWindow 是判断"集体失败"的时间窗：窗口内多把密钥因同一原因
	// 失败，才认为是配置或账号层面的问题。
	collectiveWindow = time.Minute

	// defaultJitterRatio 是退避抖动幅度：避免多把密钥同时到点、同时冲击下游。
	defaultJitterRatio = 0.2
)
