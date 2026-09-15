package search

import (
	"strings"
	"unicode"
)

// isCJK 判断 rune 是否属于 CJK 统一表意文字（基本区 + 扩展 A + 兼容区）。
// 中文无词边界，检索必须切 bigram（见 Tokenize）。
func isCJK(r rune) bool {
	switch {
	case r >= 0x4E00 && r <= 0x9FFF: // 基本区
		return true
	case r >= 0x3400 && r <= 0x4DBF: // 扩展 A
		return true
	case r >= 0xF900 && r <= 0xFAFF: // 兼容表意文字
		return true
	}
	return false
}

// Tokenize 把文本切成检索词元：
//   - ASCII 字母数字连续段：整体小写保留（"GitHub" → "github"）
//   - CJK 连续段：unigram + bigram（"搜索引擎" → 搜,索,引,擎,搜索,索引,引擎）
//
// 重复词元保留（BM25 需要真实词频）。纯标点/空白不产出词元。
func Tokenize(text string) []string {
	var (
		out   []string
		ascii strings.Builder
		cjk   []rune
	)
	flushASCII := func() {
		if ascii.Len() > 0 {
			out = append(out, ascii.String())
			ascii.Reset()
		}
	}
	flushCJK := func() {
		for i, r := range cjk {
			out = append(out, string(r))
			if i+1 < len(cjk) {
				out = append(out, string(r)+string(cjk[i+1]))
			}
		}
		cjk = cjk[:0]
	}
	for _, r := range text {
		switch {
		case isCJK(r):
			flushASCII()
			cjk = append(cjk, r)
		case unicode.IsLetter(r) || unicode.IsNumber(r):
			flushCJK()
			ascii.WriteRune(unicode.ToLower(r))
		default:
			flushASCII()
			flushCJK()
		}
	}
	flushASCII()
	flushCJK()
	return out
}

// Dedupe 去掉重复词元，保持首次出现顺序（keyword 命中判定用）。
func Dedupe(tokens []string) []string {
	seen := make(map[string]struct{}, len(tokens))
	out := make([]string, 0, len(tokens))
	for _, t := range tokens {
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	return out
}
