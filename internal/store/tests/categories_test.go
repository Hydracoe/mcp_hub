package tests

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/hydra/mcp-hub/internal/downstream"
	"github.com/hydra/mcp-hub/internal/store/categories"
	"github.com/hydra/mcp-hub/internal/store/servers"
	"github.com/hydra/mcp-hub/internal/store/storeutil"
)

func newCategories(t *testing.T) *categories.Store {
	t.Helper()
	return newTestStore(t).Categories
}

// TestCreateCategoryNeedsBothNames 中文名与英文名都是必填，且英文名不合规
// 直接报错、不做任何改写。
//
// 缺中文名 → 界面无处显示；英文名是筛选参数与工具名前缀段。
// 两者都是用户的输入问题，必须映射成 400 而不是 500。
//
// 这组用例钉住"不静默替换"：大写、空格、下划线、中文、超长全部报错，
// 而不是被折成 weather / web-search / 截断版。落库的值必须与用户填的
// 完全一致（除首尾空白），否则用户照着界面再改一次会改不动。
func TestCreateCategoryNeedsBothNames(t *testing.T) {
	s := newCategories(t)
	ctx := context.Background()

	if _, err := s.Create(ctx, "  ", "weather", ""); !errors.Is(err, categories.ErrCategoryNameRequired) {
		t.Errorf("空中文名应报 ErrCategoryNameRequired，实际 %v", err)
	}
	for _, slug := range []string{
		"", "   ", "天气", "!!!",
		"Weather", "WebSearch", // 大写不折成小写
		"web search", "web_search", "web.search", "web/search", // 分隔符不折成连字符
		"天气 weather",   // 中文掺杂不丢弃字符
		"web--search",  // 连续连字符
		"-web", "web-", // 首尾连字符
		strings.Repeat("a", 33), // 超长不截断（上限 32）
	} {
		if _, err := s.Create(ctx, "天气", slug, ""); !errors.Is(err, categories.ErrCategorySlugInvalid) {
			t.Errorf("英文名 %q 应报 ErrCategorySlugInvalid，实际 %v", slug, err)
		}
	}

	// 合法的英文名原样落库（首尾空白不算内容）。
	cat, err := s.Create(ctx, "天气", "  web-search2  ", "")
	if err != nil {
		t.Fatalf("建分类失败: %v", err)
	}
	if cat.Slug != "web-search2" {
		t.Errorf("落库的 slug = %q, want web-search2（原样，不做任何改写）", cat.Slug)
	}
}

// TestCategoryNamesAreUnique 中文名与英文名各自唯一（创建与编辑两条路径）。
//
// 英文名唯一不为工具名服务（对外工具名里只有 serverKey 与工具名），
// 而是不让"两个分类叫同一个英文名"这种梳理不清的状态建出来——
// 按分类筛选时会命中两份语义。
func TestCategoryNamesAreUnique(t *testing.T) {
	s := newCategories(t)
	ctx := context.Background()

	first, err := s.Create(ctx, "天气", "weather", "")
	if err != nil {
		t.Fatalf("建第一个分类失败: %v", err)
	}
	if _, err := s.Create(ctx, "天气", "weather2", ""); !errors.Is(err, categories.ErrCategoryTaken) {
		t.Fatalf("重复中文名应报 ErrCategoryTaken，实际 %v", err)
	}
	if _, err := s.Create(ctx, "气象", "weather", ""); !errors.Is(err, categories.ErrCategorySlugTaken) {
		t.Fatalf("重复英文名应报 ErrCategorySlugTaken，实际 %v", err)
	}

	// 编辑撞车：把第二个分类的名字改成第一个的，两条都要被拦。
	second, err := s.Create(ctx, "气象", "climate", "")
	if err != nil {
		t.Fatalf("建第二个分类失败: %v", err)
	}
	if _, err := s.Update(ctx, second.ID, "气象", first.Slug, ""); !errors.Is(err, categories.ErrCategorySlugTaken) {
		t.Errorf("编辑成他人英文名应报 ErrCategorySlugTaken，实际 %v", err)
	}
	if _, err := s.Update(ctx, second.ID, first.Name, "climate", ""); !errors.Is(err, categories.ErrCategoryTaken) {
		t.Errorf("编辑成他人中文名应报 ErrCategoryTaken，实际 %v", err)
	}
	// 空名字同样不许落到编辑路径上。
	if _, err := s.Update(ctx, second.ID, "  ", "climate", ""); !errors.Is(err, categories.ErrCategoryNameRequired) {
		t.Errorf("编辑成空中文名应报 ErrCategoryNameRequired，实际 %v", err)
	}
	if _, err := s.Update(ctx, second.ID, "气象", "Climate", ""); !errors.Is(err, categories.ErrCategorySlugInvalid) {
		t.Errorf("编辑成不合规英文名应报 ErrCategorySlugInvalid，实际 %v", err)
	}
	// 自己改自己（值不变）不算撞车。
	if _, err := s.Update(ctx, second.ID, "气象", " climate ", ""); err != nil {
		t.Errorf("保留自己的英文名不应报错，实际 %v", err)
	}
}

// TestDeleteCategoryRequiresNoServers 分类下有 server 时不能删。
//
// 删分类顺手把 server 摘成"未分类"是一次用户没点过的批量改动：
// 界面上只是若干行的分类列空掉，没人会把它和刚才那次点击联系起来。
// 必须报错（ErrCategoryInUse），且分类行与 server 的引用都不许动。
func TestDeleteCategoryRequiresNoServers(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	cat, err := s.Categories.Create(ctx, "天气", "weather", "")
	if err != nil {
		t.Fatalf("建分类失败: %v", err)
	}
	other, err := s.Categories.Create(ctx, "地图", "map", "")
	if err != nil {
		t.Fatalf("建分类失败: %v", err)
	}

	cfg := servers.Config{Spec: testSpec(), Enabled: true, CategoryID: cat.ID}
	cfg.Spec.Key = "wx"
	cfg.Spec.Name = "wx"
	if err := s.Servers.Upsert(ctx, cfg); err != nil {
		t.Fatalf("写入 server 失败: %v", err)
	}

	if err := s.Categories.Delete(ctx, cat.ID); !errors.Is(err, categories.ErrCategoryInUse) {
		t.Fatalf("分类下还有 server 应报 ErrCategoryInUse，实际 %v", err)
	}
	if _, err := s.Categories.Get(ctx, cat.ID); err != nil {
		t.Fatalf("删除被拒后分类必须还在: %v", err)
	}
	sv, err := s.Servers.GetByKey(ctx, "wx")
	if err != nil || sv.CategoryID != cat.ID {
		t.Fatalf("删除被拒后 server 的引用不该变: %+v err=%v", sv, err)
	}

	// 空分类可以删；删过的 id 变成不存在（404 而不是 409）。
	if err := s.Categories.Delete(ctx, other.ID); err != nil {
		t.Fatalf("空分类应可删除: %v", err)
	}
	if err := s.Categories.Delete(ctx, other.ID); !errors.Is(err, storeutil.ErrNotFound) {
		t.Fatalf("删不存在的分类应报 ErrNotFound，实际 %v", err)
	}

	// 把 server 搬走后即可删除。
	if err := s.Servers.Upsert(ctx, servers.Config{Spec: sv.Spec, Enabled: true, CategoryID: ""}); err != nil {
		t.Fatalf("清空 server 分类失败: %v", err)
	}
	if err := s.Categories.Delete(ctx, cat.ID); err != nil {
		t.Fatalf("摘空引用后应可删除: %v", err)
	}
}

// TestCategoryToolCounts 分类计数只统计实际归属的 server 及其工具。
func TestCategoryToolCounts(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	cat, err := s.Categories.Create(ctx, "天气", "weather", "")
	if err != nil {
		t.Fatalf("建分类失败: %v", err)
	}
	other, err := s.Categories.Create(ctx, "地图", "map", "")
	if err != nil {
		t.Fatalf("建分类失败: %v", err)
	}

	// 两个 server 归到同一个分类，一个未分类。
	for _, spec := range []struct {
		key      string
		category string
		tools    int
	}{
		{"wx", cat.ID, 2},
		{"weather2", cat.ID, 1},
		{"geo", other.ID, 3},
		{"loose", "", 4},
	} {
		cfg := servers.Config{Spec: testSpec(), Enabled: true}
		cfg.Spec.Key = spec.key
		cfg.Spec.Name = spec.key
		cfg.CategoryID = spec.category
		if err := s.Servers.Upsert(ctx, cfg); err != nil {
			t.Fatalf("写入 server %s 失败: %v", spec.key, err)
		}
		saved, err := s.Servers.GetByKey(ctx, spec.key)
		if err != nil {
			t.Fatalf("回读 server %s 失败: %v", spec.key, err)
		}
		// 工具缓存只能由探活写入——SaveProbe 是它的唯一入口。
		probe := okProbe()
		probe.Tools = probe.Tools[:0]
		for i := 0; i < spec.tools; i++ {
			probe.Tools = append(probe.Tools, downstream.ToolInfo{
				Name:        "t" + strconv.Itoa(i),
				Description: "工具 " + strconv.Itoa(i),
			})
		}
		probe.ToolCount = spec.tools
		if err := s.Servers.SaveProbe(ctx, saved.Spec.ID, probe); err != nil {
			t.Fatalf("写入 server %s 的工具失败: %v", spec.key, err)
		}
	}

	cats, err := s.Categories.List(ctx)
	if err != nil {
		t.Fatalf("读分类失败: %v", err)
	}
	got := map[string][2]int{}
	for _, c := range cats {
		got[c.Name] = [2]int{c.ServerCount, c.ToolCount}
	}
	if got["天气"] != [2]int{2, 3} {
		t.Errorf("天气 的 (server, tool) = %v, want [2 3]", got["天气"])
	}
	if got["地图"] != [2]int{1, 3} {
		t.Errorf("地图 的 (server, tool) = %v, want [1 3]", got["地图"])
	}
	// 未分类的 server 不计入任何分类——它没有 category_id 可归。
	for _, c := range cats {
		if c.ServerCount == 0 && c.ToolCount == 0 {
			continue
		}
		if c.Name != "天气" && c.Name != "地图" {
			t.Errorf("意外分类 %q 带上了计数 %d/%d", c.Name, c.ServerCount, c.ToolCount)
		}
	}
}
