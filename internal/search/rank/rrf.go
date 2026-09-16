package rank

// DefaultK 是 RRF 融合常量 k 的社区默认值（Carter et al. 2009）。
const DefaultK = 60

// RRF 是多路排名的倒数排名融合：score(d) = Σ 1/(k + rank_i(d))，rank 从 1
// 起。各路只需给出有序 ID 列表，无需对齐分数量纲（BM25 分数和 cosine 距离
// 直接混加没有意义，RRF 只吃名次）。lists 里允许同一 ID 出现在多路。
//
// 前置条件：k > 0。k 是平滑常数，k <= 0 时 1/(k+rank+1) 的分母可能为 0，
// 公式本身失去定义——此时返回 nil，不替调用方挑一个默认值。
//
// 同一个列表内重复出现的 ID 只计一次：上游去重疏漏会让该条的名次被重复
// 累加，凭空排到前面。
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
