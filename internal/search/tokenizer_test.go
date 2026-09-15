package search

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

func TestDedupeKeepsFirstOrder(t *testing.T) {
	got := Dedupe([]string{"b", "a", "b", "c", "a"})
	want := []string{"b", "a", "c"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Dedupe = %v, want %v", got, want)
	}
}
