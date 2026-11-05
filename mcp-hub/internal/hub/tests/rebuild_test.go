package tests

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hydra/mcp-hub/internal/hub/retrieval"
)

// ---- 向量重建的并发与取消语义 ----------------------------------------------
//
// 这两个用例盯住 retrieval.EmbedManager.Rebuild 的两条契约：
//
//  1. 同一时刻只有一次重建在跑，重复调用必须明确告诉调用方"已有重建在
//     进行"（skipped），不能静默返回一个会被误读成"已完成"的进度快照；
//  2. 重建全程吃调用方传入的 ctx：调用方取消后，在途的 embed 请求必须
//     随之取消，这次重建以失败收场。
//
// 时序全部用 channel 控制，不靠 sleep 竞速。

// blockingEmbed 是一个"卡住"的 embed 服务：收到请求先发信号，然后一直
// 阻塞，直到被显式放行（releaseAll）或调用方断开（请求 ctx 结束）。
type blockingEmbed struct {
	mu      sync.Mutex
	calls   int
	started chan struct{}
	release chan struct{}
	dropped chan struct{}

	releaseOnce sync.Once
}

func newBlockingEmbed(t *testing.T) (*blockingEmbed, string) {
	t.Helper()
	b := &blockingEmbed{
		started: make(chan struct{}, 8),
		release: make(chan struct{}),
		dropped: make(chan struct{}, 8),
	}
	ts := httptest.NewServer(b.handler())
	t.Cleanup(ts.Close)
	// 失败路径也要让请求解开，否则后台 goroutine 会挂到测试结束。
	t.Cleanup(b.releaseAll)
	return b, ts.URL
}

// releaseAll 放行所有在等的 embed 请求（可重复调用）。
func (b *blockingEmbed) releaseAll() {
	b.releaseOnce.Do(func() { close(b.release) })
}

func (b *blockingEmbed) callCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.calls
}

func (b *blockingEmbed) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/embeddings", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		b.mu.Lock()
		b.calls++
		b.mu.Unlock()

		select {
		case b.started <- struct{}{}:
		default:
		}

		select {
		case <-b.release:
		case <-r.Context().Done():
			// 调用方取消了：证明 ctx 真的传到了 HTTP 请求这一层。
			select {
			case b.dropped <- struct{}{}:
			default:
			}
			return
		}

		data := make([]map[string]any, len(body.Input))
		for i := range body.Input {
			data[i] = map[string]any{"index": i, "embedding": []float64{1, 0, 0}}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"model": body.Model, "data": data})
	})
	return mux
}

// TestRebuildSkipsWhenOneIsRunning 覆盖"已有重建在跑"时的显式语义：
// 第二次调用必须立刻拿到 skipped，而不是一个会被误读成"已完成"的快照。
func TestRebuildSkipsWhenOneIsRunning(t *testing.T) {
	st, svc := openSvc(t)
	ctx := context.Background()
	seedSearchServer(t, svc, "wx")

	be, modelURL := newBlockingEmbed(t)
	em := retrieval.NewEmbedManager(st, nil, retrieval.EmbedOptions{
		BaseURL: modelURL, APIKey: "k", Model: "test-model", Dimensions: 3,
	})

	firstDone := make(chan retrieval.RebuildStatus, 1)
	go func() { firstDone <- em.Rebuild(ctx) }()

	// 等第一次重建真的进了 embed（同步点，不靠 sleep）。
	select {
	case <-be.started:
	case <-time.After(10 * time.Second):
		t.Fatal("第一次重建没有发出 embed 请求")
	}

	// 第二次调用：必须立刻返回"已有重建在进行"，既不能阻塞等待，
	// 也不能真的又跑一遍重建。
	secondDone := make(chan retrieval.RebuildStatus, 1)
	go func() { secondDone <- em.Rebuild(ctx) }()

	var st2 retrieval.RebuildStatus
	select {
	case st2 = <-secondDone:
	case <-time.After(10 * time.Second):
		t.Fatal("第二次 Rebuild 阻塞了：已有重建在跑时应立刻返回 skipped")
	}

	if st2.State != retrieval.RebuildSkipped {
		t.Fatalf("第二次调用应返回 skipped，实际 %+v", st2)
	}
	if st2.Ready {
		t.Fatalf("跳过的调用不应判定就绪: %+v", st2)
	}
	if !strings.Contains(st2.Message, "跳过") {
		t.Fatalf("跳过时应说明原因，实际 message=%q", st2.Message)
	}
	if n := be.callCount(); n != 1 {
		t.Fatalf("第二次调用不应触发新的 embed 请求，实际 %d 次", n)
	}

	// 第一次重建照常跑完（跳过的调用没有把它打断）。
	be.releaseAll()
	select {
	case st1 := <-firstDone:
		if st1.State != retrieval.RebuildDone {
			t.Fatalf("第一次重建应成功: %+v", st1)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("第一次重建没有结束")
	}
	if got, _ := st.Embeddings.List(ctx); len(got) != 2 {
		t.Fatalf("应落 2 条向量，实际 %d", len(got))
	}
}

// TestRebuildPropagatesContextCancellation 覆盖 ctx 取消：取消调用方后，
// 在途的 embed 请求必须被取消，重建以失败收场，不写向量也不假装成功。
func TestRebuildPropagatesContextCancellation(t *testing.T) {
	st, svc := openSvc(t)
	seedSearchServer(t, svc, "wx")

	be, modelURL := newBlockingEmbed(t)
	em := retrieval.NewEmbedManager(st, nil, retrieval.EmbedOptions{
		BaseURL: modelURL, APIKey: "k", Model: "test-model", Dimensions: 3,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan retrieval.RebuildStatus, 1)
	go func() { done <- em.Rebuild(ctx) }()

	select {
	case <-be.started:
	case <-time.After(10 * time.Second):
		t.Fatal("重建没有发出 embed 请求")
	}

	cancel()

	// 服务端看到连接被断开 = ctx 传到了重建过程里。
	select {
	case <-be.dropped:
	case <-time.After(10 * time.Second):
		t.Fatal("取消 ctx 后 embed 请求没有断开：ctx 没有传到重建过程")
	}

	var rs retrieval.RebuildStatus
	select {
	case rs = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("取消 ctx 后重建没有结束")
	}
	if rs.State != retrieval.RebuildFailed {
		t.Fatalf("取消后应判定失败，实际 %+v", rs)
	}
	if rs.Ready {
		t.Fatalf("取消后不应判定就绪: %+v", rs)
	}
	if got, _ := st.Embeddings.List(context.Background()); len(got) != 0 {
		t.Fatalf("取消后不应写入向量，实际 %d 条", len(got))
	}
}
