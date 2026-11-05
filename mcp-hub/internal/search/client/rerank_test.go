package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRerankerHappyPath(t *testing.T) {
	var gotBody rerankRequest
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/v1/rerank") {
			t.Errorf("path = %q", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("请求不是 JSON: %v", err)
		}
		fmt.Fprint(w, `{"results":[{"index":2,"relevance_score":0.9},{"index":0,"relevance_score":0.5}]}`)
	}))
	defer srv.Close()

	rk := NewReranker("sk-test", srv.URL+"/v1", "test-rerank")
	got, err := rk.Rerank(context.Background(), "查天气", []string{"d0", "d1", "d2"}, 10)
	if err != nil {
		t.Fatalf("Rerank: %v", err)
	}
	if len(got) != 2 || got[0].Index != 2 || got[0].Score != 0.9 || got[1].Index != 0 {
		t.Fatalf("结果错误: %v", got)
	}
	if gotAuth != "Bearer sk-test" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
	if gotBody.Model != "test-rerank" || gotBody.Query != "查天气" || len(gotBody.Documents) != 3 {
		t.Fatalf("请求体错误: %+v", gotBody)
	}
	// topN 超过文档数应钳到文档数
	if gotBody.TopN != 3 {
		t.Fatalf("top_n 应钳到 %d, got %d", 3, gotBody.TopN)
	}
}

func TestRerankerUpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprint(w, `{"message":"boom"}`)
	}))
	defer srv.Close()

	rk := NewReranker("k", srv.URL+"/v1", "m")
	if _, err := rk.Rerank(context.Background(), "q", []string{"d"}, 1); err == nil {
		t.Fatal("502 应返回错误")
	}
}

// 服务端返回乱序、重复、越界的 index：客户端必须自己规范化，
// 不能让"服务端返回不可信"扩散到每个调用点。
func TestRerankerNormalizesResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"results":[
			{"index":0,"relevance_score":0.1},
			{"index":2,"relevance_score":0.9},
			{"index":2,"relevance_score":0.9},
			{"index":99,"relevance_score":0.95}
		]}`)
	}))
	defer srv.Close()

	// 末尾斜杠应被去掉，否则会请求到 //rerank
	rk := NewReranker("k", srv.URL+"/v1/", "m")
	if rk.BaseURL != srv.URL+"/v1" {
		t.Fatalf("BaseURL = %q, 末尾斜杠应被去掉", rk.BaseURL)
	}
	got, err := rk.Rerank(context.Background(), "q", []string{"d0", "d1", "d2"}, 10)
	if err != nil {
		t.Fatalf("Rerank: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("重复与越界应被去掉，剩 2 条, got %v", got)
	}
	if got[0].Index != 2 || got[1].Index != 0 {
		t.Fatalf("应按分数降序返回: %v", got)
	}
}

func TestRerankerRejectsNonPositiveTopN(t *testing.T) {
	rk := NewReranker("k", "http://example.invalid/v1", "m")
	if _, err := rk.Rerank(context.Background(), "q", []string{"d"}, 0); err == nil {
		t.Fatal("topN=0 应报错，而不是被改成某个默认条数")
	}
}
