package search

import "math"

// Cosine 返回两个向量的余弦相似度，范围 [-1,1]。
//
// 维度不一致或零向量返回 0：混维度的比较没有定义，
// 而"看起来不相干"（0）比报错更适合出现在检索的降级路径里。
func Cosine(a, b []float64) float64 {
	if len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += a[i] * b[i]
		na += a[i] * a[i]
		nb += b[i] * b[i]
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}
