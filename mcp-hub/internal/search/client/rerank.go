package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// rerankTimeout 是单次 rerank 请求的超时上限。
const rerankTimeout = 30 * time.Second

// RankedDoc 是 rerank 的一条结果：原始下标 + 相关度分。
type RankedDoc struct {
	Index int     `json:"index"`
	Score float64 `json:"relevance_score"`
}

// Reranker 是硅基流动 /v1/rerank 客户端（Jina 风格，go-openai 无此接口，手搓）。
type Reranker struct {
	client  *http.Client
	BaseURL string // 形如 https://api.siliconflow.cn/v1，末尾不带斜杠
	APIKey  string
	Model   string
}

// NewReranker 构造 rerank 客户端。baseURL 末尾的斜杠会被去掉——
// 客户端自己拼 /rerank，留着斜杠就会请求到 //rerank。
func NewReranker(apiKey, baseURL, model string) *Reranker {
	return &Reranker{
		client:  &http.Client{Timeout: rerankTimeout},
		BaseURL: strings.TrimRight(baseURL, "/"),
		APIKey:  apiKey,
		Model:   model,
	}
}

type rerankRequest struct {
	Model     string   `json:"model"`
	Query     string   `json:"query"`
	Documents []string `json:"documents"`
	TopN      int      `json:"top_n"`
}

type rerankResponse struct {
	Results []RankedDoc `json:"results"`
}

// Rerank 对 docs 精排，返回按相关度降序的 topN 条（带原始下标）。
//
// 结果在这里就规范化：丢掉越界下标、按 index 去重、按分数降序排序、
// 截断 topN。这几件事曾经留给调用方做，等于把"服务端返回不可信"这件事
// 复制给每一个调用点，而且注释承诺的"降序"其实没人保证——服务端返回什么
// 顺序就透传什么顺序。
func (r *Reranker) Rerank(ctx context.Context, query string, docs []string, topN int) ([]RankedDoc, error) {
	if len(docs) == 0 {
		return nil, nil
	}
	if topN <= 0 {
		return nil, fmt.Errorf("rerank topN 必须为正，收到 %d", topN)
	}
	if topN > len(docs) {
		// "取前 topN 条"里 topN 超过文档总数，字面含义就是全部文档。
		topN = len(docs)
	}

	body, err := json.Marshal(rerankRequest{
		Model:     r.Model,
		Query:     query,
		Documents: docs,
		TopN:      topN,
	})
	if err != nil {
		return nil, fmt.Errorf("rerank 编码请求: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.BaseURL+"/rerank", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("rerank 构造请求: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+r.APIKey)
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("rerank 请求: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("rerank 读响应: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("rerank HTTP %d: %s", resp.StatusCode, truncate(raw, 300))
	}
	var parsed rerankResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("rerank 解析响应: %w", err)
	}

	results := make([]RankedDoc, 0, len(parsed.Results))
	seen := make(map[int]struct{}, len(parsed.Results))
	for _, d := range parsed.Results {
		if d.Index < 0 || d.Index >= len(docs) {
			continue // 越界下标指向不了任何文档，只能丢
		}
		if math.IsNaN(d.Score) {
			return nil, errors.New("rerank 返回了 NaN 相关度")
		}
		if _, dup := seen[d.Index]; dup {
			continue
		}
		seen[d.Index] = struct{}{}
		results = append(results, d)
	}
	slices.SortFunc(results, func(a, b RankedDoc) int {
		switch {
		case a.Score > b.Score:
			return -1
		case a.Score < b.Score:
			return 1
		case a.Index < b.Index:
			return -1
		case a.Index > b.Index:
			return 1
		}
		return 0
	})
	if len(results) > topN {
		results = results[:topN]
	}
	return results, nil
}

// truncate 把响应体截成便于打日志的片段，按字符截以避免切出半个 UTF-8 字符。
func truncate(b []byte, n int) string {
	s := string(b)
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	i := 0
	for idx := range s {
		if i == n {
			return s[:idx] + "..."
		}
		i++
	}
	return s
}
