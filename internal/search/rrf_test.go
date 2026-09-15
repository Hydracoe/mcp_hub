package search

import "testing"

func TestRRFBoostsDocumentsInBothLists(t *testing.T) {
	hits := RRF(DefaultRRFK, [][]string{
		{"a", "b", "c"},
		{"b", "a", "d"},
	})
	if len(hits) != 4 {
		t.Fatalf("融合后应有 4 个文档, got %v", hits)
	}
	// a、b 两路都命中，名次靠前
	if hits[0].ID != "a" && hits[0].ID != "b" {
		t.Fatalf("双路命中文档应排最前, got %v", hits)
	}
	// a 是第一路第 1 + 第二路第 2；b 是第一路第 2 + 第二路第 1 → 并列
	if hits[0].Score != hits[1].Score {
		t.Fatalf("a/b 名次对调应同分, got %v", hits)
	}
	// 单路靠后的排最后
	if hits[len(hits)-1].ID != "d" {
		t.Fatalf("仅一路第 4 的应垫底, got %v", hits)
	}
}

func TestRRFSingleListIsRankOrder(t *testing.T) {
	hits := RRF(DefaultRRFK, [][]string{{"x", "y", "z"}})
	if len(hits) != 3 || hits[0].ID != "x" || hits[2].ID != "z" {
		t.Fatalf("单路应保持名次序, got %v", hits)
	}
}

func TestRRFEmpty(t *testing.T) {
	if hits := RRF(DefaultRRFK, [][]string{}); len(hits) != 0 {
		t.Fatalf("空输入应空输出, got %v", hits)
	}
	if hits := RRF(0, [][]string{{"a"}}); len(hits) != 1 {
		t.Fatalf("k<=0 应回退默认, got %v", hits)
	}
}
