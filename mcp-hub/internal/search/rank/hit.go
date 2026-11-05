// Package rank 提供排序与融合的通用原语：Hit、排序与 topK 选择、RRF 融合与余弦相似度。
package rank

import (
	"container/heap"
	"slices"
)

// Hit 是一条带分数的检索结果，lexical 打分与 RRF 融合共用。
type Hit struct {
	ID    string
	Score float64
}

// better 报告 a 是否应排在 b 前面：分数高的在前，同分按 ID 升序。
// 同分时用 ID 决定顺序，因此这是一个全序（ID 唯一），排序无需 stable。
func better(a, b Hit) bool {
	if a.Score != b.Score {
		return a.Score > b.Score
	}
	return a.ID < b.ID
}

// cmpHit 把 better 适配成 slices.SortFunc 要求的三值比较。
func cmpHit(a, b Hit) int {
	switch {
	case better(a, b):
		return -1
	case better(b, a):
		return 1
	default:
		return 0
	}
}

// SortHits 按分数降序、同分按 ID 升序排序。
func SortHits(hits []Hit) {
	slices.SortFunc(hits, cmpHit)
}

// TopK 返回 hits 中最优的 k 条（顺序同 SortHits），不修改入参。
// 用大小为 k 的最小堆做选择，复杂度 O(n log k)，避免为前几十条排序全部命中。
// k <= 0 按字面返回 nil —— "要 0 条"就是 0 条，不替换成某个默认条数。
func TopK(hits []Hit, k int) []Hit {
	if k <= 0 || len(hits) == 0 {
		return nil
	}
	if len(hits) <= k {
		out := slices.Clone(hits)
		SortHits(out)
		return out
	}

	// 堆里始终留最优的 k 条，堆顶是其中最差的一条。
	h := worstHeap(slices.Clone(hits[:k]))
	heap.Init(&h)
	for _, x := range hits[k:] {
		if better(x, h[0]) {
			h[0] = x
			heap.Fix(&h, 0)
		}
	}
	out := []Hit(h)
	SortHits(out)
	return out
}

// worstHeap 是"最差者在顶"的最小堆：分数低者在顶，同分时 ID 大者在顶。
type worstHeap []Hit

func (h worstHeap) Len() int { return len(h) }

// Less 报告 h[i] 是否算更差——更差的要更靠近堆顶。
func (h worstHeap) Less(i, j int) bool { return better(h[j], h[i]) }

func (h worstHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }

func (h *worstHeap) Push(x any) { *h = append(*h, x.(Hit)) }

func (h *worstHeap) Pop() any {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[:n-1]
	return x
}
