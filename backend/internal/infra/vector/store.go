// Package vector 提供向量编码与相似度计算。
//
// 为什么自己写而不引入向量数据库：题库场景的规模很明确——
// 一个学科的知识点通常在几百到几千个量级。这个规模下暴力计算余弦相似度
// 只需要几毫秒，而引入 ChromaDB / Milvus 会带来一个必须独立部署的服务，
// 直接破坏"单二进制、零外部依赖"这个目标（也是桌面版成立的前提）。
//
// 诚实的边界：内存里放 5 万个 768 维向量约 150MB，再往上就该换真正的
// 向量库了。这个数字写在这里，方便后来人判断是否到了该换的时候。
package vector

import (
	"encoding/binary"
	"fmt"
	"math"
	"sort"
)

// 内存占用参考阈值。
const (
	// InMemoryVectorLimit 建议的内存向量上限（条目数）。
	InMemoryVectorLimit = 50000
	// bytesPerFloat32 float32 的字节宽度。
	bytesPerFloat32 = 4
)

// Encode 把向量编码为小端字节序的字节串，用于落库。
//
// 用二进制而不是 JSON：768 维的向量 JSON 化后约 15KB，
// 二进制只要 3KB，而且省掉解析开销。几千个向量就是几十倍的差距。
func Encode(v []float32) []byte {
	out := make([]byte, len(v)*bytesPerFloat32)
	for i, f := range v {
		binary.LittleEndian.PutUint32(out[i*bytesPerFloat32:], math.Float32bits(f))
	}
	return out
}

// Decode 解码向量。
func Decode(b []byte) ([]float32, error) {
	if len(b)%bytesPerFloat32 != 0 {
		return nil, fmt.Errorf("vector: 字节长度 %d 不是 float32 的整数倍", len(b))
	}
	out := make([]float32, len(b)/bytesPerFloat32)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*bytesPerFloat32:]))
	}
	return out, nil
}

// Cosine 计算两个向量的余弦相似度，范围 [-1, 1]。
//
// 余弦而不是欧氏距离：不同模型的向量模长差异很大，
// 我们关心的是"方向是否接近"，余弦对模长不敏感。
// 零向量返回 0 而不是 NaN——NaN 会污染整个排序结果。
func Cosine(a, b []float32) float32 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	// 用 float64 累加：768 维 float32 直接累加会有明显精度损失。
	return float32(dot / (math.Sqrt(normA) * math.Sqrt(normB)))
}

// Item 一条待检索的向量。
type Item struct {
	// ID 业务 id（这里用知识点 id）。
	ID uint
	// Vector 向量。
	Vector []float32
	// Meta 附加信息，检索结果原样带回。
	Meta map[string]any
}

// Scored 带分数的检索结果。
type Scored struct {
	ID    uint
	Score float32
	Meta  map[string]any
}

// Search 返回与 query 最相似的 k 条。
//
// 暴力全量扫描 + 堆排序。几千条向量时耗时在毫秒级，
// 比维护一个近似索引（以及它带来的一致性负担）划算得多。
// minScore 之下的结果会被过滤：语义检索的"不相关"往往是
// 分数不高但仍在 topK 里，不过滤会让模型收到一堆噪音。
func Search(query []float32, items []Item, k int, minScore float32) []Scored {
	if len(query) == 0 || len(items) == 0 || k <= 0 {
		return nil
	}

	scored := make([]Scored, 0, len(items))
	for i := range items {
		s := Cosine(query, items[i].Vector)
		if s < minScore {
			continue
		}
		scored = append(scored, Scored{ID: items[i].ID, Score: s, Meta: items[i].Meta})
	}

	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].Score == scored[j].Score {
			// 分数相同时按 id 排序，保证结果稳定可复现。
			return scored[i].ID < scored[j].ID
		}
		return scored[i].Score > scored[j].Score
	})

	if len(scored) > k {
		scored = scored[:k]
	}
	return scored
}

// Normalize 对向量做 L2 归一化。
//
// 检索前把库里的向量和查询向量都归一化，之后余弦相似度退化为点积，
// 省掉每次比较时的两次开方。
func Normalize(v []float32) []float32 {
	var sum float64
	for _, f := range v {
		sum += float64(f) * float64(f)
	}
	if sum == 0 {
		return v
	}
	norm := math.Sqrt(sum)
	out := make([]float32, len(v))
	for i, f := range v {
		out[i] = float32(float64(f) / norm)
	}
	return out
}

// SameDim 校验维度一致。
func SameDim(a, b []float32) bool { return len(a) == len(b) && len(a) > 0 }
