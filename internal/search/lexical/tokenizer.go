// Package lexical 内存词法检索：中英混合分词、BM25 打分与 keyword 保序命中。
package lexical

import (
	"strings"
	"unicode"
)

// isCJKIdeograph 判断 rune 是否属于 CJK 统一表意文字。
func isCJKIdeograph(r rune) bool {
	switch {
	case r >= 0x4E00 && r <= 0x9FFF: // 基本区
		return true
	case r >= 0x3400 && r <= 0x4DBF: // 扩展 A
		return true
	case r >= 0x20000 && r <= 0x2A6DF: // 扩展 B
		return true
	case r >= 0xF900 && r <= 0xFAFF: // 兼容表意文字
		return true
	}
	return false
}

// needsBigram 判断 rune 是否属于"没有词边界"的脚本：整段保留会退化成
// "一整句一个词元"，只能切 unigram + bigram。中文、日文假名、韩文谚文
// 同属此类——假名与谚文原先落进了"字母数字整段保留"的分支，等于不切分。
func needsBigram(r rune) bool {
	if isCJKIdeograph(r) {
		return true
	}
	return unicode.In(r, unicode.Hiragana, unicode.Katakana, unicode.Hangul)
}

// Tokenize 把文本切成检索词元：
//   - 无词边界脚本（中日韩）连续段：unigram + bigram
//     （"搜索引擎" → 搜,索,引,擎,搜索,索引,引擎）
//   - 其余字母数字连续段：整体小写保留（"GitHub" → "github"）。
//     注意这不是"ASCII 段"——全角数字、西里尔字母、希腊字母同样落在这里，
//     它们有词边界或无法用 bigram 表达，整段保留是唯一不丢信息的选择。
//
// 重复词元保留（BM25 需要真实词频）。纯标点/空白不产出词元。
func Tokenize(text string) []string {
	var (
		out    []string
		word   strings.Builder
		bigram []rune
	)
	flushWord := func() {
		if word.Len() > 0 {
			out = append(out, word.String())
			word.Reset()
		}
	}
	flushBigram := func() {
		for i, r := range bigram {
			out = append(out, string(r))
			if i+1 < len(bigram) {
				out = append(out, string(r)+string(bigram[i+1]))
			}
		}
		bigram = bigram[:0]
	}
	for _, r := range text {
		switch {
		case needsBigram(r):
			flushWord()
			bigram = append(bigram, r)
		case unicode.IsLetter(r) || unicode.IsNumber(r):
			flushBigram()
			word.WriteRune(unicode.ToLower(r))
		default:
			flushWord()
			flushBigram()
		}
	}
	flushWord()
	flushBigram()
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
