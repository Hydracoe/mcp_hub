package rank

import (
	"math"
	"testing"
)

func TestCosine(t *testing.T) {
	got, err := Cosine([]float64{1, 0}, []float64{1, 0})
	if err != nil || math.Abs(got-1) > 1e-9 {
		t.Fatalf("同向向量应得 1: %v (err=%v)", got, err)
	}
	got, err = Cosine([]float64{1, 0}, []float64{0, 1})
	if err != nil || math.Abs(got) > 1e-9 {
		t.Fatalf("正交向量应得 0: %v (err=%v)", got, err)
	}
	got, err = Cosine([]float64{1, 1}, []float64{-1, -1})
	if err != nil || math.Abs(got+1) > 1e-9 {
		t.Fatalf("反向向量应得 -1: %v (err=%v)", got, err)
	}
}

// 维度不一致、空向量、零向量都必须报错：这里曾经返回 0，于是一旦维度配错，
// 调用方拿到一片 0 分却毫无察觉——检索与自动分类静默失效。
func TestCosineRejectsInvalidInput(t *testing.T) {
	cases := map[string][2][]float64{
		"维度不一致": {{1, 2, 3}, {1, 2}},
		"左边为空":  {{}, {1}},
		"右边为空":  {{1}, {}},
		"左边零向量": {{0, 0}, {1, 1}},
		"右边零向量": {{1, 1}, {0, 0}},
	}
	for name, in := range cases {
		if _, err := Cosine(in[0], in[1]); err == nil {
			t.Errorf("%s 应返回错误", name)
		}
	}
}

func TestNormalizeAndDot(t *testing.T) {
	unit, err := Normalize([]float64{3, 4})
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if math.Abs(unit[0]-0.6) > 1e-9 || math.Abs(unit[1]-0.8) > 1e-9 {
		t.Fatalf("Normalize = %v, want [0.6 0.8]", unit)
	}
	// 单位向量之间的点积就是余弦相似度
	got, err := Dot(unit, unit)
	if err != nil || math.Abs(got-1) > 1e-9 {
		t.Fatalf("Dot = %v (err=%v), want 1", got, err)
	}
	if _, err := Normalize([]float64{0, 0}); err == nil {
		t.Fatal("零向量应无法归一化")
	}
	if _, err := Normalize(nil); err == nil {
		t.Fatal("空向量应无法归一化")
	}
	if _, err := Dot([]float64{1}, []float64{1, 2}); err == nil {
		t.Fatal("维度不一致的 Dot 应报错")
	}
}
