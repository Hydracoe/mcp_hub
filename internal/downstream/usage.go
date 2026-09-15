package downstream

import (
	"encoding/json"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
)

// usageKeys 是各家声明 token 用量时用的字段名。
//
// 这是一处无法回避的启发式：MCP 规范没有约定"这次调用用了多少 token"
// 该放在哪里，各家自定义。取不到时的行为是明确的——按未计量处理，
// 而不是按 0，因为 0 会让界面显示"0 / 100k"一片祥和。
var usageKeys = []string{
	"totaltokens", "total_tokens",
	"totalTokens", "total",
}

// ExtractTokenUsage 从工具调用结果里提取下游声明的 token 用量。
//
// 查找顺序：structuredContent.usage → _meta.usage，各自再按 usageKeys
// 取总量。返回 -1 表示下游没有声明。
//
// 只在这一层做启发式，是因为它是唯一同时看得到 structuredContent
// 与 _meta 的地方；把它推给密钥池，池子就得反过来依赖协议细节。
func ExtractTokenUsage(res *mcp.CallToolResult) int {
	if res == nil {
		return -1
	}
	if n := usageIn(res.StructuredContent); n >= 0 {
		return n
	}
	if res.Meta != nil {
		if n := usageIn(res.Meta); n >= 0 {
			return n
		}
	}
	return -1
}

// usageIn 在一个任意结构里寻找 usage 节点。
func usageIn(v any) int {
	raw, err := json.Marshal(v)
	if err != nil || len(raw) == 0 {
		return -1
	}

	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return -1
	}
	return findUsage(decoded, 0)
}

// findUsage 递归查找 usage 节点，深度上限防止畸形报文把栈撑爆。
func findUsage(v any, depth int) int {
	if depth > 6 {
		return -1
	}
	switch node := v.(type) {
	case map[string]any:
		for k, child := range node {
			if strings.EqualFold(k, "usage") {
				if n := totalFromUsage(child); n >= 0 {
					return n
				}
			}
		}
		for _, child := range node {
			if n := findUsage(child, depth+1); n >= 0 {
				return n
			}
		}
	case []any:
		for _, child := range node {
			if n := findUsage(child, depth+1); n >= 0 {
				return n
			}
		}
	}
	return -1
}

// totalFromUsage 从一个 usage 对象里取总量。
func totalFromUsage(v any) int {
	node, ok := v.(map[string]any)
	if !ok {
		return -1
	}
	for _, key := range usageKeys {
		raw, ok := node[key]
		if !ok {
			continue
		}
		if f, ok := raw.(float64); ok && f >= 0 {
			return int(f)
		}
	}
	return -1
}
