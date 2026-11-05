package downstream

import (
	"errors"
	"fmt"
)

// ErrClosed 表示连接已被主动关闭，不再接受调用。
var ErrClosed = errors.New("downstream 连接已关闭")

// ErrDraining 表示连接正在停用，已不接受新调用。
var ErrDraining = errors.New("downstream 连接正在停用")

// CallError 是带分类的下游调用错误。
//
// 上层据此决定 HTTP 状态码、日志字段与给 AI 的可读说明，而不是抛一个裸 error。
type CallError struct {
	Kind   ErrorKind
	Stage  Stage
	Err    error
	Stderr string // 仅 stdio

	// Obs 是本次调用自己的 HTTP 观测（状态码、Retry-After、响应体片段）。
	// 带着它走，是为了让上层密钥调度不必再猜：判决要看的内容在错误被
	// mcp-go 包装成字符串后就没了。
	Obs Observation
}

func (e *CallError) Error() string {
	if e.Err == nil {
		return string(e.Kind)
	}
	return fmt.Sprintf("%s: %v", e.Kind, e.Err)
}

// Unwrap 让 errors.Is / errors.As 能穿透到原始错误。
func (e *CallError) Unwrap() error { return e.Err }

// CallErrorKind 从任意错误中提取分类，取不到时返回 KindUnknown。
func CallErrorKind(err error) ErrorKind {
	var ce *CallError
	if errors.As(err, &ce) {
		return ce.Kind
	}
	// 密钥池打空不是"下游坏了"，但有明确分类。漏掉这一步，用户看到的是
	// "未知错误"：本可执行的说明（还有几把在冷却、约多久后重试）会被降级成
	// 一句无用的"稍后重试即可"。
	var noKey *ErrNoUsableKey
	if errors.As(err, &noKey) {
		return KindNoUsableKey
	}
	return KindUnknown
}

// CallErrorObservation 从任意错误中提取本次调用的 HTTP 观测。
func CallErrorObservation(err error) Observation {
	var ce *CallError
	if errors.As(err, &ce) {
		return ce.Obs
	}
	return Observation{}
}
