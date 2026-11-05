package registry

import (
	"encoding/json"
	"strings"
	"testing"
)

// 这一组用例守的是聚合装载里最靠近实现的一层：schema 的兜底与降级。
// 它们直接调包内纯函数（normalizeSchema / maxSchemaBytes），因此留在本包
// 做白盒测试；走导出 API 的用例（路由、重名取舍、顺序抖动）在 ../tests
// 黑盒目录。

// --- T12：schema 兜底与降级 -------------------------------------------------

// 超大 schema 降级为提示性描述：宁可少信息也不撑爆 AI 的上下文。
func TestNormalizeSchemaDowngradesOversized(t *testing.T) {
	big := `{"type":"object","properties":{` + strings.Repeat(`"p":"x",`, 20*1024) + `"z":1}}`
	raw := json.RawMessage(big)
	if len(raw) <= maxSchemaBytes {
		t.Fatalf("构造的 schema 不够大: %d", len(raw))
	}

	got := normalizeSchema(raw)
	if len(got) > 1024 {
		t.Fatalf("降级后仍有 %d 字节，没起到省上下文的作用", len(got))
	}
	if !json.Valid(got) {
		t.Fatalf("降级结果不是合法 JSON: %s", got)
	}
	var shape struct {
		Description string `json:"description"`
	}
	if err := json.Unmarshal(got, &shape); err != nil {
		t.Fatalf("解析降级结果失败: %v", err)
	}
	// 提示必须说清两件事：为什么没了、去哪看完整的。
	for _, want := range []string{"过大", "省略", "管理界面"} {
		if !strings.Contains(shape.Description, want) {
			t.Errorf("降级提示缺 %q: %q", want, shape.Description)
		}
	}
}

// 畸形 / 空 schema 退化成空对象：不让一个坏工具拖垮整批装载。
func TestNormalizeSchemaRepairsInvalid(t *testing.T) {
	cases := map[string]json.RawMessage{
		"空":    nil,
		"空字节":  json.RawMessage(``),
		"空白":   json.RawMessage(`   `),
		"非法":   json.RawMessage(`{"type":`),
		"不是对象": json.RawMessage(`[1,2,3]`),
		"裸字符串": json.RawMessage(`"oops"`),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			got := normalizeSchema(raw)
			if string(got) != `{"type":"object"}` {
				t.Errorf("%s 的兜底 = %s, want 空对象", name, got)
			}
		})
	}
}

// 正常 schema 逐字节透传：结构化重建只会有损。
func TestNormalizeSchemaPassesThroughValid(t *testing.T) {
	raw := json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}},"required":["q"]}`)
	if got := normalizeSchema(raw); string(got) != string(raw) {
		t.Errorf("合法 schema 被改写: %s", got)
	}
}
