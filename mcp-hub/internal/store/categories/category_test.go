package categories

import (
	"errors"
	"strings"
	"testing"
)

// TestCheckCategorySlug 钉住英文名的校验规则：只判合规、不做任何改写。
// 白盒测试不依赖数据库。核心断言是合法值原样返回，否则用户照界面改回去会改不动，
// 两个不同的输入还会撞成同一个 slug。
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
