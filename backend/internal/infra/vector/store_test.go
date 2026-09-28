package vector

import (
	"math"
	"testing"
)

func TestEncodeDecodeRoundTrip(t *testing.T) {
	cases := [][]float32{
		{},
		{1},
		{0, 0, 0},
		{1.5, -2.25, 3.125, 0},
		make([]float32, 768),
	}
	// 给 768 维的填点值。
	for i := range cases[4] {
		cases[4][i] = float32(i) * 0.001
	}

	for _, in := range cases {
		encoded := Encode(in)
		if len(encoded) != len(in)*4 {
			t.Fatalf("编码长度应为 %d，实际 %d", len(in)*4, len(encoded))
		}
		out, err := Decode(encoded)
		if err != nil {
			t.Fatalf("解码失败: %v", err)
		}
		if len(out) != len(in) {
			t.Fatalf("长度不一致: %d vs %d", len(out), len(in))
		}
		for i := range in {
			if out[i] != in[i] {
				t.Fatalf("第 %d 个元素不一致: %v vs %v", i, out[i], in[i])
			}
		}
	}
}

func TestDecodeRejectsMalformed(t *testing.T) {
	for _, bad := range [][]byte{{1}, {1, 2, 3}, make([]byte, 7)} {
		if _, err := Decode(bad); err == nil {
			t.Fatalf("长度 %d 应被拒绝", len(bad))
		}
	}
}

func TestCosineIdenticalAndOrthogonal(t *testing.T) {
	a := []float32{1, 2, 3}

	// 完全相同 → 1
	if got := Cosine(a, a); math.Abs(float64(got)-1) > 1e-6 {
		t.Fatalf("相同向量余弦应为 1，实际 %v", got)
	}
	// 反向 → -1
	neg := []float32{-1, -2, -3}
	if got := Cosine(a, neg); math.Abs(float64(got)+1) > 1e-6 {
		t.Fatalf("反向向量余弦应为 -1，实际 %v", got)
	}
	// 正交 → 0
	if got := Cosine([]float32{1, 0}, []float32{0, 1}); math.Abs(float64(got)) > 1e-6 {
		t.Fatalf("正交向量余弦应为 0，实际 %v", got)
	}
}

// 余弦对模长不敏感，这是选它而不是欧氏距离的原因。
func TestCosineIgnoresMagnitude(t *testing.T) {
	a := []float32{1, 2, 3}
	b := []float32{10, 20, 30}
	if got := Cosine(a, b); math.Abs(float64(got)-1) > 1e-6 {
		t.Fatalf("同方向不同模长应得 1，实际 %v", got)
	}
}

// 零向量必须返回 0 而不是 NaN，否则会污染整个排序。
func TestCosineHandlesZeroAndMismatch(t *testing.T) {
	if got := Cosine([]float32{0, 0}, []float32{1, 1}); got != 0 {
		t.Fatalf("零向量应返回 0，实际 %v", got)
	}
	if got := Cosine(nil, []float32{1}); got != 0 {
		t.Fatal("空向量应返回 0")
	}
	if got := Cosine([]float32{1, 2}, []float32{1}); got != 0 {
		t.Fatal("维度不一致应返回 0")
	}
	for _, v := range []float32{
		Cosine([]float32{0, 0}, []float32{0, 0}),
		Cosine(nil, nil),
	} {
		if math.IsNaN(float64(v)) {
			t.Fatal("绝不能返回 NaN")
		}
	}
}

func TestSearchOrdersBySimilarity(t *testing.T) {
	items := []Item{
		{ID: 1, Vector: []float32{1, 0, 0}},
		{ID: 2, Vector: []float32{0.9, 0.1, 0}},
		{ID: 3, Vector: []float32{0, 1, 0}},
		{ID: 4, Vector: []float32{0.5, 0.5, 0}},
	}
	got := Search([]float32{1, 0, 0}, items, 3, -1)

	if len(got) != 3 {
		t.Fatalf("应返回 3 条，实际 %d", len(got))
	}
	if got[0].ID != 1 {
		t.Fatalf("最相似的应是 id=1，实际 %d", got[0].ID)
	}
	// 分数必须单调不增。
	for i := 1; i < len(got); i++ {
		if got[i].Score > got[i-1].Score {
			t.Fatalf("结果未按相似度排序: %v", got)
		}
	}
}

// minScore 用于过滤噪音：语义检索的"不相关"常常仍在 topK 里。
func TestSearchAppliesMinScore(t *testing.T) {
	items := []Item{
		{ID: 1, Vector: []float32{1, 0}},
		{ID: 2, Vector: []float32{0, 1}},
	}
	if got := Search([]float32{1, 0}, items, 10, 0.5); len(got) != 1 || got[0].ID != 1 {
		t.Fatalf("应只返回分数达标的那条，实际 %v", got)
	}
	if got := Search([]float32{1, 0}, items, 10, -1); len(got) != 2 {
		t.Fatalf("阈值为 -1 时应都返回，实际 %d 条", len(got))
	}
}

func TestSearchStableTieBreak(t *testing.T) {
	// 完全相同的向量：应按 id 升序，保证结果可复现。
	items := []Item{
		{ID: 5, Vector: []float32{1, 0}},
		{ID: 2, Vector: []float32{1, 0}},
		{ID: 9, Vector: []float32{1, 0}},
	}
	got := Search([]float32{1, 0}, items, 3, -1)
	want := []uint{2, 5, 9}
	for i := range want {
		if got[i].ID != want[i] {
			t.Fatalf("同分时未按 id 排序: %v", got)
		}
	}
}

func TestSearchEdgeCases(t *testing.T) {
	items := []Item{{ID: 1, Vector: []float32{1, 0}}}
	if got := Search(nil, items, 5, -1); got != nil {
		t.Fatal("空查询应返回 nil")
	}
	if got := Search([]float32{1, 0}, nil, 5, -1); got != nil {
		t.Fatal("空库应返回 nil")
	}
	if got := Search([]float32{1, 0}, items, 0, -1); got != nil {
		t.Fatal("k<=0 应返回 nil")
	}
}

func TestSearchCarriesMeta(t *testing.T) {
	items := []Item{{
		ID:     7,
		Vector: []float32{1, 0},
		Meta:   map[string]any{"name": "函数单调性", "path": "/1/7/"},
	}}
	got := Search([]float32{1, 0}, items, 1, -1)
	if len(got) != 1 {
		t.Fatal("应返回 1 条")
	}
	if got[0].Meta["name"] != "函数单调性" {
		t.Fatalf("应带回附加信息: %v", got[0].Meta)
	}
}

func TestNormalizeGivesUnitLength(t *testing.T) {
	v := Normalize([]float32{3, 4})
	want := []float32{0.6, 0.8}
	for i := range want {
		if math.Abs(float64(v[i]-want[i])) > 1e-6 {
			t.Fatalf("归一化结果错误: %v", v)
		}
	}
	// 归一化后自身余弦为 1。
	if got := Cosine(v, v); math.Abs(float64(got)-1) > 1e-6 {
		t.Fatalf("归一化向量自相似应为 1，实际 %v", got)
	}
	// 零向量原样返回，不产生 NaN。
	z := Normalize([]float32{0, 0})
	if z[0] != 0 || math.IsNaN(float64(z[0])) {
		t.Fatalf("零向量归一化不应产生 NaN: %v", z)
	}
}

// 归一化之后，点积就等于余弦——这是在实际检索里省掉两次开方的前提。
func TestNormalizedDotEqualsCosine(t *testing.T) {
	a := Normalize([]float32{1, 2, 3, 4})
	b := Normalize([]float32{4, 3, 2, 1})
	var dot float32
	for i := range a {
		dot += a[i] * b[i]
	}
	if math.Abs(float64(dot-Cosine(a, b))) > 1e-6 {
		t.Fatalf("归一化后点积应等于余弦: %v vs %v", dot, Cosine(a, b))
	}
}
