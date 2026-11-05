package rank

// DefaultK 是 RRF 融合常量 k 的社区默认值（Carter et al. 2009）。
const DefaultK = 60

// RRF 是多路排名的倒数排名融合：score(d) = Σ 1/(k + rank_i(d))，rank 从 1 起。
// 各路只需给出有序 ID 列表，无需对齐分数量纲（BM25 分数与 cosine 距离不能直接混加）。
// 前置条件 k > 0，否则分母可能为 0，返回 nil 而非默认值；同一列表内重复 ID 只计一次。
func RRF(k int, lists [][]string) []Hit {
	if k <= 0 {
		return nil
	}
	scores := make(map[string]float64)
	for _, list := range lists {
		seen := make(map[string]struct{}, len(list))
		for rank, id := range list {
			if _, dup := seen[id]; dup {
				continue
			}
			seen[id] = struct{}{}
			scores[id] += 1 / float64(k+rank+1)
		}
	}
	hits := make([]Hit, 0, len(scores))
	for id, s := range scores {
		hits = append(hits, Hit{ID: id, Score: s})
	}
	SortHits(hits)
	return hits
}
