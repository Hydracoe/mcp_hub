package logs

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// likePattern 是纯函数，留在本包做白盒测试；
// 依赖数据库的用例统一放在 ../tests 黑盒目录（避免 import cycle）。
func TestLikePattern(t *testing.T) {
	for _, c := range []struct {
		in   string
		want string
		why  string
	}{
		{"", "", "空输入不筛"},
		{"   ", "", "全空白同样不筛（手敲 curl 很容易带上空格）"},
		{"abc", "%abc%", "普通词两侧加通配符"},
		{" abc ", "%abc%", "两端空白丢掉"},
		{"100%", `%100\%%`, "% 必须转义，否则变成前缀匹配"},
		{"a_b", `%a\_b%`, "_ 必须转义，否则匹配任意单字符"},
		{`a\b`, `%a\\b%`, "反斜杠自己要转，且不能再被后面两条规则转一遍"},
		{`\%`, `%\\\%%`, "转义符 + 通配符连写"},
	} {
		if got := likePattern(c.in); got != c.want {
			t.Errorf("likePattern(%q) = %q, want %q（%s）", c.in, got, c.want, c.why)
		}
	}

	// 超长词按 rune 截断：劈成半个字符会让 ILIKE 拿到无效 UTF-8。
	patted := likePattern(strings.Repeat("汉", maxLogSearchRunes+50))
	if !utf8.ValidString(patted) {
		t.Error("截断后不是合法 UTF-8")
	}
	if n := utf8.RuneCountInString(strings.Trim(patted, "%")); n != maxLogSearchRunes {
		t.Errorf("截断后 rune 数 = %d, want %d", n, maxLogSearchRunes)
	}
}
