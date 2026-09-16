package rank

import "testing"

func TestSortHitsDescendingTieByID(t *testing.T) {
	hits := []Hit{
		{ID: "c", Score: 1},
		{ID: "a", Score: 3},
		{ID: "b", Score: 1},
		{ID: "d", Score: 2},
	}
	SortHits(hits)
	want := []string{"a", "d", "b", "c"}
	for i, id := range want {
		if hits[i].ID != id {
			t.Fatalf("排序结果 %v, want %v", hits, want)
		}
	}
}

// TopK 走最小堆选择：条数与顺序都必须和全量排序的前缀完全一致。
func TestTopKEqualsFullSortPrefix(t *testing.T) {
	hits := make([]Hit, 0, 500)
	for i := 0; i < 500; i++ {
		hits = append(hits, Hit{ID: string(rune('a'+i%26)) + string(rune('a'+i/26)), Score: float64(i % 37)})
	}
	all := append([]Hit(nil), hits...)
	SortHits(all)

	for _, k := range []int{1, 5, 36, 499, 500, 501} {
		got := TopK(hits, k)
		want := k
		if want > len(all) {
			want = len(all)
		}
		if len(got) != want {
			t.Fatalf("TopK(%d) 返回 %d 条, want %d", k, len(got), want)
		}
		for i := range got {
			if got[i] != all[i] {
				t.Fatalf("TopK(%d) 第 %d 条 = %v, 期望 %v", k, i, got[i], all[i])
			}
		}
	}
}

func TestTopKDoesNotMutateInput(t *testing.T) {
	hits := []Hit{{ID: "a", Score: 1}, {ID: "b", Score: 5}, {ID: "c", Score: 3}}
	before := append([]Hit(nil), hits...)
	TopK(hits, 2)
	for i := range hits {
		if hits[i] != before[i] {
			t.Fatalf("TopK 修改了入参: %v, want %v", hits, before)
		}
	}
}

func TestTopKNonPositiveIsEmpty(t *testing.T) {
	hits := []Hit{{ID: "a", Score: 1}}
	// 按字面执行："要 0 条"就是 0 条，不替换成默认条数。
	if got := TopK(hits, 0); got != nil {
		t.Fatalf("k=0 应返回 nil, got %v", got)
	}
	if got := TopK(hits, -3); got != nil {
		t.Fatalf("k<0 应返回 nil, got %v", got)
	}
}
