package search

import (
	"context"
	"fmt"
	"time"

	openai "github.com/sashabaranov/go-openai"
)

// DefaultEmbedBatch 是硅基流动 embed 接口的批量上限。
const DefaultEmbedBatch = 64

// Embedder 是 OpenAI 兼容的 /v1/embeddings 客户端（硅基流动）。
type Embedder struct {
	client *openai.Client
	Model  string
	Batch  int
}

// NewEmbedder 构造 embed 客户端。baseURL 形如 https://api.siliconflow.cn/v1；
// batch ≤0 时取默认 64。
func NewEmbedder(apiKey, baseURL, model string, batch int) *Embedder {
	if batch <= 0 {
		batch = DefaultEmbedBatch
	}
	cfg := openai.DefaultConfig(apiKey)
	cfg.BaseURL = baseURL
	return &Embedder{
		client: openai.NewClientWithConfig(cfg),
		Model:  model,
		Batch:  batch,
	}
}

// Embed 对 texts 分批调 embed 接口，返回与输入同序的向量。任一批失败即
// 整体返回错误（调用方按 PRD 保留旧向量、只 warn）。
func (e *Embedder) Embed(ctx context.Context, texts []string) ([][]float64, error) {
	out := make([][]float64, 0, len(texts))
	for start := 0; start < len(texts); start += e.Batch {
		end := start + e.Batch
		if end > len(texts) {
			end = len(texts)
		}
		batch := texts[start:end]
		resp, err := e.client.CreateEmbeddings(ctx, openai.EmbeddingRequestStrings{
			Model: openai.EmbeddingModel(e.Model),
			Input: batch,
		})
		if err != nil {
			return nil, fmt.Errorf("embed batch %d-%d: %w", start, end, err)
		}
		if len(resp.Data) != len(batch) {
			return nil, fmt.Errorf("embed batch %d-%d: 返回 %d 条向量，期望 %d",
				start, end, len(resp.Data), len(batch))
		}
		byIndex := make(map[int][]float64, len(resp.Data))
		for _, d := range resp.Data {
			v := make([]float64, len(d.Embedding))
			for i, f := range d.Embedding {
				v[i] = float64(f)
			}
			byIndex[d.Index] = v
		}
		for i := range batch {
			v, ok := byIndex[i]
			if !ok {
				return nil, fmt.Errorf("embed batch %d-%d: 缺少 index=%d 的向量", start, end, i)
			}
			out = append(out, v)
		}
	}
	return out, nil
}

// Ping 发一条最小 embed 验证 key/model 可用（启动自检用）。
func (e *Embedder) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, err := e.Embed(ctx, []string{"ping"})
	return err
}
