package rank

import (
	"errors"
	"fmt"
	"math"
)

// Cosine 返回两个向量的余弦相似度，范围 [-1,1]。
// 长度不等、空向量、零向量一律返回错误，不返回 0：算不出来必须让调用方看见，
// 否则维度配错时一片 0 分会让检索与自动分类静默失效。
func Cosine(a, b []float64) (float64, error) {
	if len(a) != len(b) {
		return 0, fmt.Errorf("向量维度不一致：%d vs %d", len(a), len(b))
	}
	if len(a) == 0 {
		return 0, errors.New("空向量没有余弦相似度")
	}
	var dot, na, nb float64
	for i := range a {
		dot += a[i] * b[i]
		na += a[i] * a[i]
		nb += b[i] * b[i]
	}
	if na == 0 || nb == 0 {
		return 0, errors.New("零向量的余弦相似度没有定义")
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb)), nil
}

// Normalize 返回 v 的单位向量。
// 查询向量与候选都预先归一化后只做 Dot，避免每比一次都重算两边的范数。
func Normalize(v []float64) ([]float64, error) {
	if len(v) == 0 {
		return nil, errors.New("空向量无法归一化")
	}
	var norm float64
	for _, f := range v {
		norm += f * f
	}
	if norm == 0 {
		return nil, errors.New("零向量无法归一化")
	}
	scale := 1 / math.Sqrt(norm)
	out := make([]float64, len(v))
	for i, f := range v {
		out[i] = f * scale
	}
	return out, nil
}

// Dot 返回两个等长向量的点积。传入已归一化的向量时，结果即余弦相似度。
func Dot(a, b []float64) (float64, error) {
	if len(a) != len(b) {
		return 0, fmt.Errorf("向量维度不一致：%d vs %d", len(a), len(b))
	}
	if len(a) == 0 {
		return 0, errors.New("空向量没有点积")
	}
	var dot float64
	for i := range a {
		dot += a[i] * b[i]
	}
	return dot, nil
}
