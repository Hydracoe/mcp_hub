package search

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEmbedderBatchingAndOrder(t *testing.T) {
	var gotBatches [][]string
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
		gotBatches = append(gotBatches, req.Input)
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

	e := NewEmbedder("k", srv.URL+"/v1", "test-model", 3)
	vecs, err := e.Embed(context.Background(), texts)
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vecs) != len(texts) {
		t.Fatalf("向量数 = %d, want %d", len(vecs), len(texts))
	}
	// 每批 ≤3 且顺序不丢
	if len(gotBatches) != 2 {
		t.Fatalf("batch=3 对 5 条应分 2 批, got %d 批", len(gotBatches))
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

	e := NewEmbedder("bad", srv.URL+"/v1", "test-model", 0)
	if _, err := e.Embed(context.Background(), []string{"x"}); err == nil {
		t.Fatal("401 应返回错误")
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
