package tests

import (
	"context"
	"testing"
	"time"

	"github.com/hydra/mcp-hub/internal/search/corpus"
	"github.com/hydra/mcp-hub/internal/store"
	"github.com/hydra/mcp-hub/internal/store/embeddings"
	"github.com/hydra/mcp-hub/internal/store/servers"
)

// seedEmbeddingServer 造一个 server 和它的探活工具，返回一个工具 id。
// tool_embeddings.tool_id 外键到 tools.id，空表写不了向量行。
func seedEmbeddingServer(t *testing.T, s *store.Store) string {
	t.Helper()
	ctx := context.Background()

	spec := testSpec()
	if err := s.Servers.Upsert(ctx, servers.Config{Spec: spec, Enabled: true}); err != nil {
		t.Fatalf("Upsert 失败: %v", err)
	}
	sv, err := s.Servers.GetByKey(ctx, spec.Key)
	if err != nil {
		t.Fatalf("GetByKey 失败: %v", err)
	}
	if err := s.Servers.SaveProbe(ctx, sv.Spec.ID, okProbe()); err != nil {
		t.Fatalf("SaveProbe 失败: %v", err)
	}
	tools, err := s.Servers.ListToolsByServer(ctx, sv.Spec.ID)
	if err != nil || len(tools) != 2 {
		t.Fatalf("ListToolsByServer 失败: %v, 得到 %d 个工具", err, len(tools))
	}
	return tools[0].ID
}

func embeddingOf(toolID, model string, dims int, spec string, vector []float64) embeddings.Embedding {
	return embeddings.Embedding{
		ToolID:     toolID,
		Model:      model,
		Dims:       dims,
		Spec:       spec,
		Vector:     vector,
		CorpusHash: "h-" + model,
		CreatedAt:  time.Now(),
	}
}

// 两代向量同表共存是热切换的地基：构建新代期间旧代还在服务检索，
// 四元组主键必须让 (tool_id, model, dims) 相同但 spec 不同的行互不覆盖。
func TestEmbeddingDualGenerationCoexist(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	toolID := seedEmbeddingServer(t, s)

	// 旧口径（无分类无标签）与新默认口径（四项全开）——两个不同的 spec。
	specOld := corpus.SpecString(corpus.Fields{ToolDescription: true, ServerDesc: true})
	specNew := corpus.SpecString(corpus.DefaultFields())
	if specOld == specNew {
		t.Fatalf("测试前提破坏：两个 spec 应当不同")
	}

	if err := s.Embeddings.Save(ctx, []embeddings.Embedding{embeddingOf(toolID, "m1", 8, specOld, []float64{1, 0})}); err != nil {
		t.Fatalf("写入旧代向量失败: %v", err)
	}
	if err := s.Embeddings.Save(ctx, []embeddings.Embedding{embeddingOf(toolID, "m1", 8, specNew, []float64{0, 1})}); err != nil {
		t.Fatalf("写入新代向量失败: %v", err)
	}

	rows, err := s.Embeddings.List(ctx)
	if err != nil {
		t.Fatalf("List 失败: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("两代应共存为 2 行，实际 %d 行", len(rows))
	}

	// 重存旧代（向量刷新）不能伤到新代。
	if err := s.Embeddings.Save(ctx, []embeddings.Embedding{embeddingOf(toolID, "m1", 8, specOld, []float64{0.5, 0.5})}); err != nil {
		t.Fatalf("重存旧代向量失败: %v", err)
	}
	rows, err = s.Embeddings.List(ctx)
	if err != nil {
		t.Fatalf("List 失败: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("重存后仍应 2 行，实际 %d 行", len(rows))
	}
	bySpec := make(map[string]embeddings.Embedding, len(rows))
	for _, r := range rows {
		bySpec[r.Spec] = r
	}
	if bySpec[specOld].Vector[0] != 0.5 {
		t.Errorf("旧代向量未更新: %v", bySpec[specOld].Vector)
	}
	if bySpec[specNew].Vector[0] != 0 {
		t.Errorf("新代向量被旧代覆盖: %v", bySpec[specNew].Vector)
	}

	if n, err := s.Embeddings.Count(ctx, "m1", 8, specOld); err != nil || n != 1 {
		t.Errorf("Count(旧代) = %d, %v; want 1", n, err)
	}
	if n, err := s.Embeddings.Count(ctx, "m1", 8, specNew); err != nil || n != 1 {
		t.Errorf("Count(新代) = %d, %v; want 1", n, err)
	}
	if n, err := s.Embeddings.Count(ctx, "m1", 8, "不存在"); err != nil || n != 0 {
		t.Errorf("Count(无此代) = %d, %v; want 0", n, err)
	}
}

// DeleteStaleGenerations 只留指定代，保留集由调用方决定。
// 生产路径只在当前代完全就绪后才保留当前代并清掉其余——没有回滚接口，清掉的旧代只能重新嵌入。
func TestDeleteStaleGenerations(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	toolID := seedEmbeddingServer(t, s)

	genA := embeddings.Generation{Model: "m1", Dims: 8, Spec: "sa"}
	genB := embeddings.Generation{Model: "m2", Dims: 8, Spec: "sa"}
	genC := embeddings.Generation{Model: "m1", Dims: 16, Spec: "sa"}
	if err := s.Embeddings.Save(ctx, []embeddings.Embedding{
		embeddingOf(toolID, genA.Model, genA.Dims, genA.Spec, []float64{1}),
		embeddingOf(toolID, genB.Model, genB.Dims, genB.Spec, []float64{2}),
		embeddingOf(toolID, genC.Model, genC.Dims, genC.Spec, []float64{3}),
	}); err != nil {
		t.Fatalf("写入三代向量失败: %v", err)
	}

	if err := s.Embeddings.DeleteStaleGenerations(ctx, genA, genB); err != nil {
		t.Fatalf("DeleteStaleGenerations 失败: %v", err)
	}
	if n, _ := s.Embeddings.Count(ctx, genC.Model, genC.Dims, genC.Spec); n != 0 {
		t.Errorf("genC 应被清理，仍有 %d 行", n)
	}
	if n, _ := s.Embeddings.Count(ctx, genA.Model, genA.Dims, genA.Spec); n != 1 {
		t.Errorf("genA 应保留，剩 %d 行", n)
	}
	if n, _ := s.Embeddings.Count(ctx, genB.Model, genB.Dims, genB.Spec); n != 1 {
		t.Errorf("genB 应保留，剩 %d 行", n)
	}

	// 再次收缩到只留 genA（下一次切换后 genB 过期）。
	if err := s.Embeddings.DeleteStaleGenerations(ctx, genA); err != nil {
		t.Fatalf("DeleteStaleGenerations 失败: %v", err)
	}
	if n, _ := s.Embeddings.Count(ctx, genB.Model, genB.Dims, genB.Spec); n != 0 {
		t.Errorf("genB 应被清理，仍有 %d 行", n)
	}

	// keep 为空 = 清全表。
	if err := s.Embeddings.DeleteStaleGenerations(ctx); err != nil {
		t.Fatalf("DeleteStaleGenerations 失败: %v", err)
	}
	rows, err := s.Embeddings.List(ctx)
	if err != nil {
		t.Fatalf("List 失败: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("清空后应无向量行，实际 %d 行", len(rows))
	}
}
