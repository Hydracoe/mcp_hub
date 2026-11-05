package corpus

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTextFieldSelection(t *testing.T) {
	f := DefaultFields()
	got := Text("search_repositories", "在 GitHub 上搜索仓库",
		[]string{"b", "a"}, "GitHub 官方 MCP 服务", "q（关键词）", f)
	want := "search repositories\n在 GitHub 上搜索仓库\nq（关键词）\na b\nGitHub 官方 MCP 服务"
	if got != want {
		t.Fatalf("Text = %q, want %q", got, want)
	}

	// 默认勾选：五项全开
	if !f.ToolName || !f.ToolDescription || !f.Tags || !f.ServerDesc || !f.SchemaHints {
		t.Fatalf("默认勾选错误: %+v", f)
	}

	// 空段不产生空行
	got = Text("", "desc", []string{"x"}, "srv", "", f)
	if got != "desc\nx\nsrv" {
		t.Fatalf("空段应跳过: %q", got)
	}

	// 未勾选的段应跳过
	got = Text("nm", "desc", nil, "srv", "", Fields{ToolName: true, ToolDescription: true})
	if got != "nm\ndesc" {
		t.Fatalf("未勾选的段应跳过: %q", got)
	}

	// 全空
	got = Text("", "", nil, "", "", DefaultFields())
	if got != "" {
		t.Fatalf("全空应为空串, got %q", got)
	}
}

// 分类不是语料字段：改一次分类名不该让任何一条向量作废。
func TestCorpusDoesNotDependOnCategory(t *testing.T) {
	f := DefaultFields()
	// 语料组装函数根本收不到分类参数——这一条由签名保证；
	// 这里额外钉住"同一工具在任何分类措辞下语料一致"的语义。
	a := Text("forecast", "查天气", nil, "weather svc", "", f)
	b := Text("forecast", "查天气", nil, "weather svc", "", f)
	if a != b || Hash(a) != Hash(b) {
		t.Fatalf("同输入语料应逐字节一致: %q vs %q", a, b)
	}
}

func TestNaturalName(t *testing.T) {
	cases := map[string]string{
		"search_repositories": "search repositories",
		"get-user-info":       "get user info",
		"a__b":                "a b",
		"plain":               "plain",
		"  spaced  out  ":     "spaced out",
	}
	for in, want := range cases {
		if got := NaturalName(in); got != want {
			t.Errorf("NaturalName(%q) = %q, want %q", in, got, want)
		}
	}
}

// 参数线索只取 properties 的键名与说明：整段 schema 里的 type/required
// 是噪声，塞进去会把真正的语义稀释掉。
func TestSchemaHints(t *testing.T) {
	schema := json.RawMessage(`{
		"type": "object",
		"required": ["city"],
		"properties": {
			"sort": {"type": "string", "description": "排序方式"},
			"city": {"type": "string", "description": "城市名"}
		}
	}`)
	got, err := SchemaHints(schema)
	if err != nil {
		t.Fatalf("SchemaHints: %v", err)
	}
	// 键序必须稳定（排序），否则每轮探活都会重嵌
	if got != "city（城市名） · sort（排序方式）" {
		t.Fatalf("SchemaHints = %q", got)
	}
	if strings.Contains(got, "type") || strings.Contains(got, "required") {
		t.Fatalf("不该带上 type/required 噪声: %q", got)
	}
	// 无 description 时只留键名，不留空括号
	if got, err := SchemaHints(json.RawMessage(`{"properties":{"repo":{"type":"string"}}}`)); err != nil || got != "repo" {
		t.Fatalf("无说明的参数应只留键名: %q (err=%v)", got, err)
	}
	// 畸形 JSON 是数据质量问题，必须报出来——它和"这个工具没有参数"是两回事。
	if _, err := SchemaHints(json.RawMessage(`{`)); err == nil {
		t.Fatal("畸形 schema 应返回错误")
	}
	// 没有 schema、以及合法但没有 properties，才是"没有参数线索"：空串 + 无错误
	if got, err := SchemaHints(nil); err != nil || got != "" {
		t.Fatalf("nil schema 应返回空且无错误: %q (err=%v)", got, err)
	}
	if got, err := SchemaHints(json.RawMessage(`{"type":"object"}`)); err != nil || got != "" {
		t.Fatalf("无 properties 应返回空且无错误: %q (err=%v)", got, err)
	}
}

// 超长时按完整单元截断：既不超过上限，也不留半个括号。
func TestSchemaHintsTruncatesOnUnitBoundary(t *testing.T) {
	props := make([]string, 0, 40)
	for i := 0; i < 40; i++ {
		props = append(props, fmt.Sprintf(
			`"param%02d":{"description":"用于撑长度的参数说明文字第%d段"}`, i, i))
	}
	schema := json.RawMessage(`{"properties":{` + strings.Join(props, ",") + `}}`)
	got, err := SchemaHints(schema)
	if err != nil {
		t.Fatalf("SchemaHints: %v", err)
	}
	if got == "" {
		t.Fatal("不该截成空串")
	}
	if n := utf8.RuneCountInString(got); n > MaxSchemaHints {
		t.Fatalf("截断后 %d 字符，超过上限 %d", n, MaxSchemaHints)
	}
	if strings.Count(got, "（") != strings.Count(got, "）") {
		t.Fatalf("括号不配平: %q", got)
	}
	if !strings.HasPrefix(got, "param00（") {
		t.Fatalf("应从第一个参数开始: %q", got)
	}
}

func TestSchemaHintsStableOrder(t *testing.T) {
	// Go 的 map 遍历是随机的：不排序的话同一份 schema 每轮组出不同文本，
	// 语料哈希抖动 → 每轮探活全量重嵌。
	schema := json.RawMessage(`{"properties":{"z":{"description":"z"},"a":{"description":"a"},"m":{"description":"m"}}}`)
	first, err := SchemaHints(schema)
	if err != nil {
		t.Fatalf("SchemaHints: %v", err)
	}
	for i := 0; i < 20; i++ {
		got, err := SchemaHints(schema)
		if err != nil {
			t.Fatalf("SchemaHints: %v", err)
		}
		if got != first {
			t.Fatalf("SchemaHints 不稳定: %q vs %q", got, first)
		}
	}
}

// 语料口径来自 MCPHUB_CORPUS_FIELDS，是从字符串解析出来的。解析错了不会报错，
// 只会悄悄改变整代向量的构建成本，所以要钉住：空/all 等于全开、顺序与大小写无关、拼错必须报错。
func TestParseFields(t *testing.T) {
	all := DefaultFields()
	for _, spec := range []string{"", "all", "ALL", " all "} {
		got, err := ParseFields(spec)
		if err != nil {
			t.Fatalf("ParseFields(%q) 报错: %v", spec, err)
		}
		if got != all {
			t.Fatalf("ParseFields(%q) = %+v，期望全开", spec, got)
		}
	}

	cases := map[string]Fields{
		"toolName":                          {ToolName: true},
		"tags,schemaHints":                  {Tags: true, SchemaHints: true},
		"schemaHints, tags":                 {Tags: true, SchemaHints: true},
		"toolDescription,serverDescription": {ToolDescription: true, ServerDesc: true},
	}
	for spec, want := range cases {
		got, err := ParseFields(spec)
		if err != nil {
			t.Fatalf("ParseFields(%q) 报错: %v", spec, err)
		}
		if got != want {
			t.Fatalf("ParseFields(%q) = %+v，期望 %+v", spec, got, want)
		}
	}

	// 拼错必须报错：静默当成"没勾"会让用户以为改了口径，实际是整代重嵌。
	// 末尾多一个逗号不算错（手写清单的常见形态），全空才算。
	for _, bad := range []string{"toolName,typo", ",", "toolname,nope"} {
		if _, err := ParseFields(bad); err == nil {
			t.Fatalf("ParseFields(%q) 应报错", bad)
		}
	}
}

func TestSpecStringStable(t *testing.T) {
	a := SpecString(DefaultFields())
	b := SpecString(DefaultFields())
	if a != b {
		t.Fatalf("SpecString 不稳定: %q vs %q", a, b)
	}
	if SpecString(Fields{ToolDescription: true}) == a {
		t.Fatal("字段不同 spec 应不同")
	}
	if SpecString(Fields{ToolName: true}) == SpecString(Fields{Tags: true}) {
		t.Fatal("不同字段开关 spec 应不同")
	}
}

// 代编码必须覆盖每一个字段：手工列举漏一个，后果是"语料口径变了但代身份
// 没变"——该重建的向量不重建，而且没有任何报错。这两条测试一起把
// "新增字段"的完整改动点钉住：进 Fields、带 json 标签、进声明表。
func TestSpecStringCoversAllFields(t *testing.T) {
	spec := SpecString(DefaultFields())
	for _, name := range FieldNames {
		if !strings.Contains(spec, name+"=") {
			t.Fatalf("SpecString 缺少字段 %s: %q", name, spec)
		}
	}
}

func TestFieldsDeclarationConsistent(t *testing.T) {
	typ := reflect.TypeOf(Fields{})
	if typ.NumField() != len(FieldNames) {
		t.Fatalf("Fields 有 %d 个字段，FieldNames 有 %d 个：新增字段没进声明表",
			typ.NumField(), len(FieldNames))
	}
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if f.Tag.Get("json") == "" {
			t.Errorf("字段 %s 缺少 json 标签，SpecString 会生成空名字段", f.Name)
		}
	}
	// 默认口径必须把每个字段都打开：默认漏一个字段，语料就少一段信息。
	for _, fs := range fieldTable {
		var f Fields
		fs.set(&f)
		if f == (Fields{}) {
			t.Errorf("字段 %s 的 set 没有真正打开它", fs.name)
		}
	}
	if DefaultFields() != (func() Fields {
		var f Fields
		for _, fs := range fieldTable {
			fs.set(&f)
		}
		return f
	})() {
		t.Fatal("DefaultFields 与声明表不一致")
	}
}

// 增量判定的依据只有语料文本本身：model / dims 属于"代身份"，
// 不是"这条语料变没变"。把它们掺进哈希会让同一段文本在两个模型下
// 得到两个哈希，掩盖"语料其实没变"这个事实。
func TestHashIncremental(t *testing.T) {
	base := Hash("查天气")
	if base != Hash("查天气") {
		t.Fatal("未变更的语料 hash 应稳定")
	}
	if Hash("查天气了") == base {
		t.Fatal("语料文本变了 hash 应变化")
	}
}
