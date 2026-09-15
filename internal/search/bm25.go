package search

import (
	"math"
	"strings"
)

// Doc 是检索文档的最小单元。Tokens 已按字段权重展开（工具名 ×3 即出现 3 次，
// BM25 的词频自然带上权重）；Raw 是小写原文，keyword 子串命中用。
type Doc struct {
	ID     string
	Tokens []string
	Raw    string
}

// Hit 是单次检索的一条结果。
type Hit struct {
	ID    string
	Score float64
}

const (
	bm25K1 = 1.2
	bm25B  = 0.75
)

// BM25 是在一批文档上建好的内存倒排索引。语料小（数百到数千工具），
// 每次查询现场构建的开销远小于 10ms 预算，无需维护常驻状态。
type BM25 struct {
	count int
	avgDl float64
	df    map[string]int
	tfs   []map[string]int
	dls   []int
	idAt  []string
}

// NewBM25 对 docs 建索引。重复 ID 以最后一次为准。
func NewBM25(docs []Doc) *BM25 {
	deduped := make([]Doc, 0, len(docs))
	pos := make(map[string]int, len(docs))
	for _, d := range docs {
		if i, ok := pos[d.ID]; ok {
			deduped[i] = d
			continue
		}
		pos[d.ID] = len(deduped)
		deduped = append(deduped, d)
	}
	ix := &BM25{
		count: len(deduped),
		df:    make(map[string]int),
		tfs:   make([]map[string]int, len(deduped)),
		dls:   make([]int, len(deduped)),
		idAt:  make([]string, len(deduped)),
	}
	total := 0
	for i, d := range deduped {
		tf := make(map[string]int, len(d.Tokens))
		for _, t := range d.Tokens {
			tf[t]++
		}
		ix.tfs[i] = tf
		ix.dls[i] = len(d.Tokens)
		ix.idAt[i] = d.ID
		total += len(d.Tokens)
		for t := range tf {
			ix.df[t]++
		}
	}
	if ix.count > 0 {
		ix.avgDl = float64(total) / float64(ix.count)
	}
	return ix
}

func (ix *BM25) idf(term string) float64 {
	df := ix.df[term]
	return math.Log(1 + (float64(ix.count)-float64(df)+0.5)/(float64(df)+0.5))
}

// Search 对 query 词元打分，返回按分数降序的 topK（分数 ≤0 的丢弃）。
func (ix *BM25) Search(queryTokens []string, topK int) []Hit {
	if topK <= 0 {
		topK = 10
	}
	scores := make([]float64, ix.count)
	for _, q := range Dedupe(queryTokens) {
		idf := ix.idf(q)
		for i, tf := range ix.tfs {
			f := float64(tf[q])
			if f == 0 {
				continue
			}
			dl := float64(ix.dls[i])
			norm := bm25K1 * (1 - bm25B + bm25B*dl/ix.avgDl)
			scores[i] += idf * f * (bm25K1 + 1) / (f + norm)
		}
	}
	hits := make([]Hit, 0, topK)
	for i, s := range scores {
		if s > 0 {
			hits = append(hits, Hit{ID: ix.idAt[i], Score: s})
		}
	}
	sortHits(hits)
	if len(hits) > topK {
		hits = hits[:topK]
	}
	return hits
}

// sortHits 按分数降序；同分按 ID 升序保证结果稳定。
func sortHits(hits []Hit) {
	for i := 1; i < len(hits); i++ {
		for j := i; j > 0; j-- {
			a, b := hits[j-1], hits[j]
			if a.Score > b.Score || (a.Score == b.Score && a.ID <= b.ID) {
				break
			}
			hits[j-1], hits[j] = b, a
		}
	}
}

// KeywordHits 返回命中 query 的文档 ID，保持输入顺序、不打分（PRD：keyword
// 保序、不做精细打分）。命中判定：任一 query 词元出现，或 query 小写原文
// 是文档小写原文的子串（覆盖连续中文短语如"查天气"整串匹配）。
func KeywordHits(query string, docs []Doc) []string {
	qTokens := make(map[string]struct{})
	for _, t := range Dedupe(Tokenize(query)) {
		qTokens[t] = struct{}{}
	}
	raw := strings.ToLower(query)
	out := make([]string, 0)
	for _, d := range docs {
		hit := false
		for _, t := range d.Tokens {
			if _, ok := qTokens[t]; ok {
				hit = true
				break
			}
		}
		if !hit && raw != "" {
			hit = strings.Contains(d.Raw, raw)
		}
		if hit {
			out = append(out, d.ID)
		}
	}
	return out
}
