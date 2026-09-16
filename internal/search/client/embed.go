// Package client 外部模型服务客户端：OpenAI 兼容 /v1/embeddings 与
// /v1/rerank（硅基流动）。除 HTTP 调用外不含任何检索逻辑。
package client

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	openai "github.com/sashabaranov/go-openai"
)

const (
	// DefaultBatch 是硅基流动 embed 接口的批量上限。
	DefaultBatch = 64

	// embedTimeout 是单次 embed 请求的超时上限。openai.DefaultConfig 给的
	// HTTPClient 没有超时，不显式设一个的话，上游 hang 住就会一直挂着——
	// 调用方未必每次都带 deadline。
	embedTimeout = 60 * time.Second

	// embedConcurrency 是分批提交的并发度。全量重建时逐批串行是纯等待，
	// 并发能把这段耗时压到几分之一，同时不至于把上游打爆。
	embedConcurrency = 4

	// pingTimeout 是启动自检的超时。
	pingTimeout = 10 * time.Second
)

// 默认服务与模型。配置默认值唯一出处：维度校验、客户端构造、启动对账
// 共用同一份常量，cmd 层不得另抄一份，避免两处漂移。
const (
	DefaultBaseURL     = "https://api.siliconflow.cn/v1"
	DefaultModel       = "BAAI/bge-m3"
	DefaultDims        = 1024
	DefaultRerankModel = "BAAI/bge-reranker-v2-m3"
)

// Embedder 是 OpenAI 兼容的 /v1/embeddings 客户端（硅基流动）。
type Embedder struct {
	client *openai.Client
	Model  string
	// Dims 是配置里声明的向量维度，逐条校验返回向量长度，不等即整体报错。
	// 这里没有"0 表示不校验"的隐式语义：维度不匹配是最隐蔽的一种坏法
	// （库里 dims 列与 vector 列对不上，检索一路降级而日志只有一行 warn），
	// 一条用数值 0 表达的旁路会让配置漏填时静默摘掉唯一的守门。
	// 确实需要跳过校验的场合（探活、测试）走 NewEmbedderUnchecked。
	Dims  int
	Batch int
}

// NewEmbedder 构造 embed 客户端。baseURL 形如 https://api.siliconflow.cn/v1。
//
// dims 与 batch 必须为正：两者都是配置项，配错就在构造点报错，
// 不拖到第一次调用、也不静默换成默认值。
func NewEmbedder(apiKey, baseURL, model string, dims, batch int) (*Embedder, error) {
	if dims <= 0 {
		return nil, errors.New("embed 维度必须为正，请检查 MCPHUB_EMBED_DIMS")
	}
	if batch <= 0 {
		return nil, errors.New("embed 批次大小必须为正，请检查 MCPHUB_EMBED_BATCH_SIZE")
	}
	return newEmbedder(apiKey, baseURL, model, dims, batch), nil
}

// NewEmbedderUnchecked 构造不做维度校验的 embed 客户端，只给探活与测试用。
// 名字里带 Unchecked 是刻意的：跳过校验意味着坏向量可以直接进库，
// 这件事必须在调用点一眼可见，而不是藏在"dims 传 0"里。
func NewEmbedderUnchecked(apiKey, baseURL, model string, batch int) *Embedder {
	if batch <= 0 {
		panic("client.NewEmbedderUnchecked: batch 必须为正")
	}
	return newEmbedder(apiKey, baseURL, model, 0, batch)
}

func newEmbedder(apiKey, baseURL, model string, dims, batch int) *Embedder {
	cfg := openai.DefaultConfig(apiKey)
	cfg.BaseURL = baseURL
	cfg.HTTPClient = &http.Client{Timeout: embedTimeout}
	return &Embedder{
		client: openai.NewClientWithConfig(cfg),
		Model:  model,
		Dims:   dims,
		Batch:  batch,
	}
}

// Embed 对 texts 分批调 embed 接口，返回与输入同序的向量。任一批失败即
// 整体返回错误（调用方按 PRD 保留旧向量、只 warn），绝不返回部分结果。
//
// 分批并发提交（embedConcurrency 上限），按批次起始下标回填以保持同序。
func (e *Embedder) Embed(ctx context.Context, texts []string) ([][]float64, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	type span struct{ start, end int }
	spans := make([]span, 0, (len(texts)+e.Batch-1)/e.Batch)
	for start := 0; start < len(texts); start += e.Batch {
		spans = append(spans, span{start: start, end: min(start+e.Batch, len(texts))})
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	out := make([][]float64, len(texts))
	sem := make(chan struct{}, embedConcurrency)
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
	)

feed:
	for _, sp := range spans {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			// 已经有一批失败并取消了 ctx，剩下的批次不必再发。
			break feed
		}
		wg.Add(1)
		go func(sp span) {
			defer wg.Done()
			defer func() { <-sem }()
			vecs, err := e.embedBatch(ctx, texts[sp.start:sp.end])
			if err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = fmt.Errorf("embed batch %d-%d: %w", sp.start, sp.end, err)
				}
				mu.Unlock()
				cancel() // 让仍在途的批次尽快返回
				return
			}
			mu.Lock()
			copy(out[sp.start:sp.end], vecs)
			mu.Unlock()
		}(sp)
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	return out, nil
}

// embedBatch 取一批文本的向量，返回与 batch 同序的结果。
func (e *Embedder) embedBatch(ctx context.Context, batch []string) ([][]float64, error) {
	resp, err := e.client.CreateEmbeddings(ctx, openai.EmbeddingRequestStrings{
		Model: openai.EmbeddingModel(e.Model),
		Input: batch,
	})
	if err != nil {
		return nil, err
	}
	if len(resp.Data) != len(batch) {
		return nil, fmt.Errorf("返回 %d 条向量，期望 %d", len(resp.Data), len(batch))
	}

	byIndex := make(map[int][]float64, len(resp.Data))
	for _, d := range resp.Data {
		// 维度不匹配是最隐蔽的一种坏法：不报错的话，库里 dims 列（配置值）
		// 与 vector 列（真实维度）就此对不上，检索一路降级而日志只有一行
		// warn。写入前拦住，坏数据进不了库。
		if e.Dims > 0 && len(d.Embedding) != e.Dims {
			return nil, fmt.Errorf(
				"模型 %q 返回 %d 维向量，与配置的 dimensions=%d 不符（换模型后请同步改 dimensions）",
				e.Model, len(d.Embedding), e.Dims)
		}
		// 越界下标不是可以忽略的噪声：它说明返回体与请求对不上，
		// 静默丢弃会让后面的 "缺少 index" 报错指向错误的原因。
		if d.Index < 0 || d.Index >= len(batch) {
			return nil, fmt.Errorf("返回越界的 index=%d（本批 %d 条）", d.Index, len(batch))
		}
		v := make([]float64, len(d.Embedding))
		for i, f := range d.Embedding {
			v[i] = float64(f)
		}
		byIndex[d.Index] = v
	}

	out := make([][]float64, len(batch))
	for i := range batch {
		v, ok := byIndex[i]
		if !ok {
			return nil, fmt.Errorf("缺少 index=%d 的向量", i)
		}
		out[i] = v
	}
	return out, nil
}

// Ping 发一条最小 embed 验证 key/model 可用（启动自检用）。
func (e *Embedder) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()
	_, err := e.Embed(ctx, []string{"ping"})
	return err
}
