// Package storeutil 是 store 及其子包共享的持久化小工具。
//
// 独立成叶子包是拆分的结果：servers/credentials/… 各自成包后，
// "记录不存在"这类跨域哨兵错误与行映射辅助必须有一个谁都能引、
// 又不构成环的落脚点。
package storeutil

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"gorm.io/gorm"
)

// ErrNotFound 表示目标记录不存在（上层映射 404）。全 store 共用一个值，
// 调用方只需 errors.Is 一处判断。
var ErrNotFound = errors.New("record not found")

// NewID 生成一个 16 字节的随机标识。
func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("crypto/rand 不可用: %v", err))
	}
	return hex.EncodeToString(b[:])
}

// RequireAffected 把"0 行受影响"折算成 ErrNotFound。
func RequireAffected(rows int64, err error, what string) error {
	if err != nil {
		return fmt.Errorf("%s失败: %w", what, err)
	}
	if rows == 0 {
		return ErrNotFound
	}
	return nil
}

// IsUniqueViolation 判断唯一约束冲突。常规撞车已被写入前预检挡住，
// 这里兜的是并发写入的竞态窗口，同样要翻译成 409 而不是 500。
func IsUniqueViolation(err error) bool {
	return err != nil && errors.Is(err, gorm.ErrDuplicatedKey)
}

func BoolToInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

func IntToBool(v int64) bool { return v != 0 }

// JSON 列都是 TEXT 存 JSON 字符串；空集合写成字面量而不是 null，
// 免得读回时区分"没填"和"填了 null"。
func JSONOrEmptyMap(m map[string]string) string {
	if len(m) == 0 {
		return "{}"
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func JSONOrEmptySlice(v any) string {
	b, err := json.Marshal(v)
	if err != nil || string(b) == "null" {
		return "[]"
	}
	return string(b)
}

func DecodeStringMap(raw string) map[string]string {
	if raw == "" {
		return nil
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil
	}
	return m
}

func DecodeStringSlice(raw string) []string {
	if raw == "" {
		return nil
	}
	var v []string
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return nil
	}
	return v
}
