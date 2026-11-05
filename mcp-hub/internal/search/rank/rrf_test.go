package rank

import (
	"math"
	"testing"
)

func TestRRFBoostsDocumentsInBothLists(t *testing.T) {
	hits := RRF(DefaultK, [][]string{
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
	hits := RRF(DefaultK, [][]string{{"x", "y", "z"}})
	if len(hits) != 3 || hits[0].ID != "x" || hits[2].ID != "z" {
		t.Fatalf("单路应保持名次序, got %v", hits)
	}
}

func TestRRFEmpty(t *testing.T) {
	if hits := RRF(DefaultK, [][]string{}); len(hits) != 0 {
		t.Fatalf("空输入应空输出, got %v", hits)
	}
	// k 是平滑常数，k<=0 时 1/(k+rank+1) 的分母可能为 0，公式失去定义：
	// 按字面返回空，而不是悄悄换成默认值。
	if hits := RRF(0, [][]string{{"a"}}); hits != nil {
		t.Fatalf("k<=0 应返回 nil, got %v", hits)
	}
}

// 同一个列表内重复的 ID 只计一次。去重的语义是"该 ID 不再重复加分"，
// 不是"把后面的条目往前挪"——所以 y 仍按它在原列表里的位置计分。
func TestRRFDedupesRepeatedIDWithinList(t *testing.T) {
	got := RRF(DefaultK, [][]string{{"x", "x", "x", "y"}})
	if len(got) != 2 {
		t.Fatalf("重复 ID 应只算一次, got %v", got)
	}
	wantX := 1 / float64(DefaultK+1)   // x 只在第 1 名记一次
	wantY := 1 / float64(DefaultK+3+1) // y 仍在第 4 位
	if got[0].ID != "x" || math.Abs(got[0].Score-wantX) > 1e-12 {
		t.Fatalf("x = %v, want %v", got[0], wantX)
	}
	if got[1].ID != "y" || math.Abs(got[1].Score-wantY) > 1e-12 {
		t.Fatalf("y = %v, want %v", got[1], wantY)
	}
	// 没有去重的话 x 会拿到三次累加，被凭空推高
	tripled := 1/float64(DefaultK+1) + 1/float64(DefaultK+2) + 1/float64(DefaultK+3)
	if math.Abs(got[0].Score-tripled) < 1e-12 {
		t.Fatal("重复 ID 被重复计分了")
	}
}
