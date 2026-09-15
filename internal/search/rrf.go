package search

// DefaultRRFK 是 RRF 融合常量的社区默认值（Carter et al. 2009）。
const DefaultRRFK = 60

// RRF 是多路排名的倒数排名融合：score(d) = Σ 1/(k + rank_i(d))，rank 从 1
// 起。各路只需给出有序 ID 列表，无需对齐分数量纲（BM25 分数和 cosine 距离
// 直接混加没有意义，RRF 只吃名次）。lists 里允许同一 ID 出现在多路。
func RRF(k int, lists [][]string) []Hit {
	if k <= 0 {
		k = DefaultRRFK
	}
	scores := make(map[string]float64)
	for _, list := range lists {
		for rank, id := range list {
			scores[id] += 1 / float64(k+rank+1)
		}
	}
	hits := make([]Hit, 0, len(scores))
	for id, s := range scores {
		hits = append(hits, Hit{ID: id, Score: s})
	}
	sortHits(hits)
	return hits
}
