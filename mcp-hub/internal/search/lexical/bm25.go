package lexical

import (
	"math"

	"github.com/hydra/mcp-hub/internal/search/rank"
)

// Doc 是检索文档的最小单元。Tokens 已按字段权重展开（工具名 ×3 即出现 3 次，
// BM25 的词频自然带上权重）。
type Doc struct {
	ID     string
	Tokens []string
}

const (
	bm25K1 = 1.2
	bm25B  = 0.75
)

// posting 是一个词元在某篇文档中的出现情况。
type posting struct {
	doc  int32
	freq int32
}

// BM25 是在一批文档上建好的内存倒排索引。语料小（数百到数千工具），
// 随聚合快照现场重建即可，无需维护常驻状态。
// 词元 → posting 列表的倒排表让打分只遍历真正含该词元的文档，而不是扫过全部文档。
type BM25 struct {
	count    int
	avgDl    float64
	idAt     []string
	dls      []int
	postings map[string][]posting
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
		count:    len(deduped),
		idAt:     make([]string, len(deduped)),
		dls:      make([]int, len(deduped)),
		postings: make(map[string][]posting, len(deduped)),
	}
	total := 0
	for i, d := range deduped {
		freq := make(map[string]int32, len(d.Tokens))
		for _, t := range d.Tokens {
			freq[t]++
		}
		ix.idAt[i] = d.ID
		ix.dls[i] = len(d.Tokens)
		total += len(d.Tokens)
		for term, f := range freq {
			ix.postings[term] = append(ix.postings[term], posting{doc: int32(i), freq: f})
		}
	}
	// count 为 0 时 avgDl 没有定义（0/0）；此时 postings 为空，
	// Search 的打分循环一次都不会进，读不到这个值。
	if ix.count > 0 {
		ix.avgDl = float64(total) / float64(ix.count)
	}
	return ix
}

// Count 返回索引里的文档数。
func (ix *BM25) Count() int { return ix.count }

// idf 用包含该词元的文档数（df）算逆文档频率。df 来自倒排表的长度。
func (ix *BM25) idf(df int) float64 {
	return math.Log(1 + (float64(ix.count)-float64(df)+0.5)/(float64(df)+0.5))
}

// Search 对 query 词元打分，返回按分数降序的 topK（分数 ≤0 的丢弃）。
// topK <= 0 时按字面返回 nil，不替换成默认条数；topK 用最小堆完成，不对全部命中排序。
func (ix *BM25) Search(queryTokens []string, topK int) []rank.Hit {
	if topK <= 0 || ix.count == 0 {
		return nil
	}
	scores := make([]float64, ix.count)
	hit := make([]bool, ix.count)
	hitN := 0
	for _, q := range Dedupe(queryTokens) {
		ps := ix.postings[q]
		if len(ps) == 0 {
			continue
		}
		idf := ix.idf(len(ps))
		for _, p := range ps {
			if !hit[p.doc] {
				hit[p.doc] = true
				hitN++
			}
			f := float64(p.freq)
			dl := float64(ix.dls[p.doc])
			norm := bm25K1 * (1 - bm25B + bm25B*dl/ix.avgDl)
			scores[p.doc] += idf * f * (bm25K1 + 1) / (f + norm)
		}
	}
	hits := make([]rank.Hit, 0, hitN)
	for i, s := range scores {
		if s > 0 {
			hits = append(hits, rank.Hit{ID: ix.idAt[i], Score: s})
		}
	}
	return rank.TopK(hits, topK)
}
