package categories

import (
	"errors"
	"strings"
	"testing"
)

// TestCheckCategorySlug 钉住英文名的校验规则：只判合规、不做任何改写。
//
// 这是白盒测试（不依赖数据库）：规则的边界比 CRUD 更容易被改松，
// 而松掉的症状很隐蔽——"保存成功了，但存下来的不是我填的那个值"。
// 核心断言是**合法值原样返回**：落库的值必须与用户填的完全一致，
// 否则用户照着界面把名字改回去会改不动，两个不同的输入还会撞成
// 同一个 slug，冲突报错指向一个看不懂的名字。
func TestCheckCategorySlug(t *testing.T) {
	valid := []struct{ in, want string }{
		{"weather", "weather"},
		{"web-search", "web-search"},
		{"a-b-c", "a-b-c"},
		{"web2", "web2"},
		{"2fa", "2fa"},
		{"a", "a"},
		{strings.Repeat("a", 32), strings.Repeat("a", 32)}, // 上限之内
		{"  weather  ", "weather"},                         // 首尾空白是输入噪声，不是内容
	}
	for _, tc := range valid {
		got, err := checkCategorySlug(tc.in)
		if err != nil {
			t.Errorf("checkCategorySlug(%q) 应通过，实际报错 %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("checkCategorySlug(%q) = %q, want %q（原样，不改写）", tc.in, got, tc.want)
		}
	}

	invalid := []string{
		"", "   ",
		"天气", "!!!", "_",
		"Weather", "WebSearch", "WEB", // 大写不折成小写
		"web search", "web_search", "web.search", "web/search", // 分隔符不折成连字符
		"天气 weather", "weather!", // 中文/符号不丢弃
		"-web", "web-", "--web", "web--search", // 连字符位置与连续性
		strings.Repeat("a", 33), // 超长不截断
	}
	for _, in := range invalid {
		if _, err := checkCategorySlug(in); !errors.Is(err, ErrCategorySlugInvalid) {
			t.Errorf("checkCategorySlug(%q) 应报 ErrCategorySlugInvalid，实际 %v", in, err)
		}
	}
}
