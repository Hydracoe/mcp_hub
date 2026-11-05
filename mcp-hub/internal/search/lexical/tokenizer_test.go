package lexical

import (
	"reflect"
	"testing"
)

func TestTokenizeASCII(t *testing.T) {
	got := Tokenize("GitHub MCP Server v2.1")
	want := []string{"github", "mcp", "server", "v2", "1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Tokenize = %v, want %v", got, want)
	}
}

func TestTokenizeCJK(t *testing.T) {
	// 顺序：逐字 unigram 后紧跟该字起的 bigram
	got := Tokenize("查天气")
	want := []string{"查", "查天", "天", "天气", "气"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Tokenize = %v, want %v", got, want)
	}
}

func TestTokenizeMixed(t *testing.T) {
	got := Tokenize("搜索 github 仓库")
	want := []string{"搜", "搜索", "索", "github", "仓", "仓库", "库"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Tokenize = %v, want %v", got, want)
	}
}

func TestTokenizePunctuationOnly(t *testing.T) {
	if got := Tokenize("!!! ??? ---"); len(got) != 0 {
		t.Fatalf("Tokenize = %v, want empty", got)
	}
}

// 日文假名与韩文谚文同样没有词边界，必须走 bigram；
// 整段保留等于不切分，检索召回会形同虚设。
func TestTokenizeKanaAndHangulUseBigram(t *testing.T) {
	got := Tokenize("さがす")
	want := []string{"さ", "さが", "が", "がす", "す"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Tokenize(假名) = %v, want %v", got, want)
	}
	got = Tokenize("검색")
	want = []string{"검", "검색", "색"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Tokenize(谚文) = %v, want %v", got, want)
	}
}

// 有词边界的非拉丁脚本整段保留，不被丢弃也不被 bigram 拆碎。
func TestTokenizeNonLatinWordScripts(t *testing.T) {
	got := Tokenize("поиск GitHub")
	want := []string{"поиск", "github"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Tokenize = %v, want %v", got, want)
	}
}

func TestDedupeKeepsFirstOrder(t *testing.T) {
	got := Dedupe([]string{"b", "a", "b", "c", "a"})
	want := []string{"b", "a", "c"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Dedupe = %v, want %v", got, want)
	}
}
