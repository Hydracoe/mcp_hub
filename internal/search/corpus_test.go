package search

import "testing"

func TestCorpusTextFieldSelection(t *testing.T) {
	f := CorpusFields{ToolDescription: true, Tags: true, ServerDesc: true}
	got := CorpusText("查天气工具", []string{"b", "a"}, "weather server 提供气象服务", f)
	want := "查天气工具\na b\nweather server 提供气象服务"
	if got != want {
		t.Fatalf("CorpusText = %q, want %q", got, want)
	}
	// 默认勾选：工具描述 + server 描述，不含 tags
	def := DefaultCorpusFields()
	if !def.ToolDescription || !def.ServerDesc || def.Tags {
		t.Fatalf("默认勾选错误: %+v", def)
	}
	got = CorpusText("desc", []string{"x"}, "srv", def)
	if got != "desc\nsrv" {
		t.Fatalf("默认语料 = %q", got)
	}
	// 空字段不产生空行
	got = CorpusText("", nil, "", CorpusFields{ToolDescription: true, Tags: true, ServerDesc: true})
	if got != "" {
		t.Fatalf("全空应为空串, got %q", got)
	}
}

func TestCorpusHashIncremental(t *testing.T) {
	base := CorpusHash("查天气", "BAAI/bge-m3", 1024)
	if base != CorpusHash("查天气", "BAAI/bge-m3", 1024) {
		t.Fatal("未变更的语料 hash 应稳定")
	}
	if CorpusHash("查天气了", "BAAI/bge-m3", 1024) == base {
		t.Fatal("语料文本变了 hash 应变化")
	}
	if CorpusHash("查天气", "other-model", 1024) == base {
		t.Fatal("模型变了 hash 应变化")
	}
	if CorpusHash("查天气", "BAAI/bge-m3", 768) == base {
		t.Fatal("维度变了 hash 应变化")
	}
}
