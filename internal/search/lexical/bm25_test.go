package lexical

import (
	"fmt"
	"testing"
)

// 语料：3 个工具，测"查天气"应命中天气工具且排第一。
func sampleDocs() []Doc {
	mk := func(id, name string, tags []string, desc string) Doc {
		var tokens []string
		for _, t := range Tokenize(name) {
			tokens = append(tokens, t, t, t) // 工具名 ×3
		}
		for _, tag := range tags {
			for _, t := range Tokenize(tag) {
				tokens = append(tokens, t, t) // tags ×2
			}
		}
		tokens = append(tokens, Tokenize(desc)...) // 描述 ×1
		return Doc{ID: id, Tokens: tokens}
	}
	return []Doc{
		mk("t1", "weather__forecast", []string{"天气"}, "查询指定城市的天气预报与实时气象数据"),
		mk("t2", "weather__alarm", []string{"预警"}, "查询气象灾害预警信息"),
		mk("t3", "map__geocode", []string{"地图"}, "地址转经纬度坐标"),
	}
}

func TestBM25RanksRelevantFirst(t *testing.T) {
	ix := NewBM25(sampleDocs())
	hits := ix.Search(Tokenize("查天气"), 10)
	if len(hits) == 0 {
		t.Fatal("Search 无命中")
	}
	if hits[0].ID != "t1" {
		t.Fatalf("第一名 = %v, want t1; 全部: %v", hits[0].ID, hits)
	}
}

func TestBM25NoFalsePositive(t *testing.T) {
	ix := NewBM25(sampleDocs())
	hits := ix.Search(Tokenize("区块链挖矿"), 10)
	if len(hits) != 0 {
		t.Fatalf("无关 query 不应命中, got %v", hits)
	}
}

func TestBM25TopK(t *testing.T) {
	docs := make([]Doc, 0, 50)
	for i := 0; i < 50; i++ {
		docs = append(docs, Doc{ID: fmt.Sprintf("doc%02d", i), Tokens: []string{"alpha"}})
	}
	ix := NewBM25(docs)
	if hits := ix.Search([]string{"alpha"}, 5); len(hits) != 5 {
		t.Fatalf("topK=5 应返回 5 条, got %d", len(hits))
	}
	if hits := ix.Search([]string{"alpha"}, 0); hits != nil {
		t.Fatalf("topK=0 应按字面返回 nil, got %v", hits)
	}
}

// topK 走最小堆选择：条数与顺序都必须和全量排序一致。
func TestBM25TopKMatchesFullSort(t *testing.T) {
	docs := make([]Doc, 0, 200)
	for i := 0; i < 200; i++ {
		// 词频不同 → 分数不同，堆选择才有区分度
		toks := make([]string, 0, 6)
		for j := 0; j <= i%6; j++ {
			toks = append(toks, "alpha")
		}
		docs = append(docs, Doc{ID: fmt.Sprintf("d%03d", i), Tokens: toks})
	}
	ix := NewBM25(docs)
	all := ix.Search([]string{"alpha"}, len(docs))
	for _, k := range []int{1, 3, 7, 20, 199} {
		got := ix.Search([]string{"alpha"}, k)
		if len(got) != k {
			t.Fatalf("topK=%d 返回 %d 条", k, len(got))
		}
		for i := range got {
			if got[i] != all[i] {
				t.Fatalf("topK=%d 第 %d 条 = %v, 全量排序为 %v", k, i, got[i], all[i])
			}
		}
	}
}

func TestBM25StableOnTie(t *testing.T) {
	docs := []Doc{
		{ID: "b", Tokens: []string{"x"}},
		{ID: "a", Tokens: []string{"x"}},
	}
	ix := NewBM25(docs)
	for i := 0; i < 3; i++ {
		hits := ix.Search([]string{"x"}, 10)
		if len(hits) != 2 || hits[0].ID != "a" || hits[1].ID != "b" {
			t.Fatalf("同分应按 ID 稳定排序, run=%d got %v", i, hits)
		}
	}
}

