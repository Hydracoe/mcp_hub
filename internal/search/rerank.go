package search

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// RankedDoc 是 rerank 的一条结果：原始下标 + 相关度分。
type RankedDoc struct {
	Index int     `json:"index"`
	Score float64 `json:"relevance_score"`
}

// Reranker 是硅基流动 /v1/rerank 客户端（Jina 风格，go-openai 无此接口，手搓）。
type Reranker struct {
	client  *http.Client
	BaseURL string // 形如 https://api.siliconflow.cn/v1
	APIKey  string
	Model   string
}

// NewReranker 构造 rerank 客户端。
func NewReranker(apiKey, baseURL, model string) *Reranker {
	return &Reranker{
		client:  &http.Client{Timeout: 30 * time.Second},
		BaseURL: baseURL,
		APIKey:  apiKey,
		Model:   model,
	}
}

type rerankRequest struct {
	Model           string   `json:"model"`
	Query           string   `json:"query"`
	Documents       []string `json:"documents"`
	TopN            int      `json:"top_n"`
	ReturnDocuments bool     `json:"return_documents"`
}

type rerankResponse struct {
	Results []RankedDoc `json:"results"`
}

// Rerank 对 docs 精排，返回按相关度降序的 topN 条（带原始下标）。
func (r *Reranker) Rerank(ctx context.Context, query string, docs []string, topN int) ([]RankedDoc, error) {
	if topN <= 0 || topN > len(docs) {
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
	return parsed.Results, nil
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "..."
}
