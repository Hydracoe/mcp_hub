package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestEmbedderBatchingAndOrder(t *testing.T) {
	var (
		mu         sync.Mutex
		gotBatches [][]string
	)
	// 全局位置表：上游按"请求内 index"返回，这里用全局位置造向量便于断言顺序
	position := map[string]int{}
	texts := []string{"a", "bb", "ccc", "dddd", "ee"}
	for i, s := range texts {
		position[s] = i
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("请求不是 JSON: %v", err)
		}
		if req.Model != "test-model" {
			t.Errorf("model = %q, want test-model", req.Model)
		}
		// 分批是并发提交的，多个 handler 会同时进来。
		mu.Lock()
		gotBatches = append(gotBatches, req.Input)
		mu.Unlock()
		data := make([]map[string]any, 0, len(req.Input))
		for i, s := range req.Input {
			data = append(data, map[string]any{
				"object":    "embedding",
				"index":     i,
				"embedding": []float32{float32(position[s]), float32(len(s))},
			})
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"object":"list","data":%s,"model":"test-model","usage":{"prompt_tokens":1,"total_tokens":1}}`, mustMarshal(t, data))
	}))
	defer srv.Close()

	e := NewEmbedderUnchecked("k", srv.URL+"/v1", "test-model", 3)
	vecs, err := e.Embed(context.Background(), texts)
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vecs) != len(texts) {
		t.Fatalf("向量数 = %d, want %d", len(vecs), len(texts))
	}
	// 每批 ≤3 且顺序不丢（并发提交下回填顺序尤其要守住）
	mu.Lock()
	batches := gotBatches
	mu.Unlock()
	if len(batches) != 2 {
		t.Fatalf("batch=3 对 5 条应分 2 批, got %d 批", len(batches))
	}
	for _, b := range batches {
		if len(b) > 3 {
			t.Fatalf("单批 %d 条，超过 batch=3", len(b))
		}
	}
	for i, v := range vecs {
		want0 := float64(i)
		if v[0] != want0 {
			t.Fatalf("第 %d 条向量乱序: %v, want[0]=%v", i, v, want0)
		}
	}
}

func TestEmbedderUpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"code":30014,"message":"Token is invalid."}`)
	}))
	defer srv.Close()

	e := NewEmbedderUnchecked("bad", srv.URL+"/v1", "test-model", 1)
	if _, err := e.Embed(context.Background(), []string{"x"}); err == nil {
		t.Fatal("401 应返回错误")
	}
}

func TestEmbedderRejectsDimsMismatch(t *testing.T) {
	// 上游返回 4 维，配置说 8 维。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"object":"list","data":[{"object":"embedding","index":0,"embedding":[1,2,3,4]}],"model":"m"}`)
	}))
	defer srv.Close()

	// dims 配错必须在写入前拦住：放过去的话库里 dims 列与 vector 列对不上，
	// 检索时 pgvector 强转失败、向量一路静默降级成 keyword+bm25。
	e, err := NewEmbedder("k", srv.URL+"/v1", "m", 8, 1)
	if err != nil {
		t.Fatalf("构造失败: %v", err)
	}
	if _, err := e.Embed(context.Background(), []string{"x"}); err == nil {
		t.Fatal("维度不符应报错")
	} else if !strings.Contains(err.Error(), "dimensions") {
		t.Fatalf("错误信息应点明 dimensions，实际: %v", err)
	}

	// dims 正确时照常通过
	ok, err := NewEmbedder("k", srv.URL+"/v1", "m", 4, 1)
	if err != nil {
		t.Fatalf("构造失败: %v", err)
	}
	if _, err := ok.Embed(context.Background(), []string{"x"}); err != nil {
		t.Fatalf("维度相符应通过: %v", err)
	}
}

// dims / batch 非法在构造点就报错。不存在"传 0 等于不校验 / 取默认"的隐式
// 语义——那种设计会让配置漏填静默摘掉唯一的维度守门。
func TestNewEmbedderRejectsNonPositiveParams(t *testing.T) {
	if _, err := NewEmbedder("k", "http://example.invalid/v1", "m", 0, 1); err == nil {
		t.Fatal("dims=0 应报错")
	}
	if _, err := NewEmbedder("k", "http://example.invalid/v1", "m", 8, 0); err == nil {
		t.Fatal("batch=0 应报错")
	}
	// 确实要跳过维度校验的场合走显式命名的构造，Dims 为 0 是它的约定。
	e := NewEmbedderUnchecked("k", "http://example.invalid/v1", "m", 1)
	if e == nil || e.Dims != 0 || e.Batch != 1 {
		t.Fatalf("Unchecked 构造应跳过维度校验: %+v", e)
	}
}

func mustMarshal(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}
