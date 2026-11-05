// Package corpus 组装工具的向量化语料：字段勾选、文本拼装、增量指纹与参数线索。
package corpus

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"reflect"
	"sort"
	"strings"
	"unicode/utf8"
)

// 语料各段的截断上限（按字符）。截断避免冗长文本稀释语义，
// 也让语料哈希对无关噪声不敏感，减少无谓的重嵌。
const (
	MaxToolDescription = 512
	MaxServerDesc      = 256
	MaxSchemaHints     = 384
	MaxParamNote       = 48
)

// Fields 勾选进向量化语料的字段。只收工具自身、创建后不再变化的字段；
// 分类等可变元数据刻意排除（改分类名不触发重嵌，它仍参与词法检索与过滤）。
// 每个字段都必须有 json 标签：SpecString 靠它生成代编码，缺标签会得到空名字段。
type Fields struct {
	ToolName        bool `json:"toolName"`
	ToolDescription bool `json:"toolDescription"`
	Tags            bool `json:"tags"`
	ServerDesc      bool `json:"serverDescription"`
	SchemaHints     bool `json:"schemaHints"`
}

// fieldSpec 把"规范名"与"在 Fields 上的开关"绑在一起。新增语料字段只改这一处：
// FieldNames、默认值、解析都从它派生，避免"加了字段却漏改某一处"的静默漂移。
type fieldSpec struct {
	name string
	set  func(*Fields)
}

var fieldTable = []fieldSpec{
	{"toolName", func(f *Fields) { f.ToolName = true }},
	{"toolDescription", func(f *Fields) { f.ToolDescription = true }},
	{"tags", func(f *Fields) { f.Tags = true }},
	{"serverDescription", func(f *Fields) { f.ServerDesc = true }},
	{"schemaHints", func(f *Fields) { f.SchemaHints = true }},
}

// FieldNames 是语料字段的规范名，同时是 MCPHUB_CORPUS_FIELDS 接受的取值，
// 也是字段声明表驱动出的唯一清单。
var FieldNames = func() []string {
	names := make([]string, 0, len(fieldTable))
	for _, fs := range fieldTable {
		names = append(names, fs.name)
	}
	return names
}()

// DefaultFields 返回默认勾选：声明表里的字段全开。
// 走表而不是手写：默认漏一个字段，语料就少一段信息，而且不会有任何报错。
func DefaultFields() Fields {
	var f Fields
	for _, fs := range fieldTable {
		fs.set(&f)
	}
	return f
}

// ParseFields 解析 MCPHUB_CORPUS_FIELDS：逗号分隔，大小写与空格不敏感；
// all 或留空表示全开。不做模糊匹配——猜错代价高，报错比猜更便宜。
func ParseFields(spec string) (Fields, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" || strings.EqualFold(spec, "all") {
		return DefaultFields(), nil
	}

	var f Fields
	for _, part := range strings.Split(spec, ",") {
		name := strings.TrimSpace(part)
		if name == "" {
			continue
		}
		known := false
		for _, fs := range fieldTable {
			if strings.EqualFold(fs.name, name) {
				fs.set(&f)
				known = true
				break
			}
		}
		if !known {
			return Fields{}, fmt.Errorf("未知的语料字段 %q（可用：%s，或 all）",
				name, strings.Join(FieldNames, ", "))
		}
	}
	if f == (Fields{}) {
		return Fields{}, fmt.Errorf("至少要勾选一个语料字段（可用：%s，或 all）",
			strings.Join(FieldNames, ", "))
	}
	return f, nil
}

// SpecString 是语料字段组合的稳定编码，与 model、dims 共同构成一代
// 向量的身份：字段组合变了即新代，须整代重建而非原地覆盖。
// 用反射按 Fields 的 json 标签生成；手工列举会导致"口径变了但代身份没变"而不报错。
func SpecString(f Fields) string {
	v := reflect.ValueOf(f)
	t := v.Type()
	parts := make([]string, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		parts = append(parts, fmt.Sprintf("%s=%t", t.Field(i).Tag.Get("json"), v.Field(i).Bool()))
	}
	return strings.Join(parts, ",")
}

// NaturalName 把工具标识符折成自然词（search_repositories → search repositories）。
// embedding 模型按自然语言训练，下划线/连字符/点/斜杠都是噪声，一律折成空格。
func NaturalName(name string) string {
	r := strings.NewReplacer("_", " ", "-", " ", ".", " ", "/", " ")
	return strings.Join(strings.Fields(r.Replace(name)), " ")
}

// SchemaHints 从 inputSchema 摘出"参数名 + 一句话说明"的参数线索，不整段塞 schema。
// 键序排序保证语料确定，否则哈希逐轮抖动导致全量重嵌。
// JSON 解析失败必须返回错误（数据质量问题），而"工具没有参数"是正常形态返回空串。
func SchemaHints(raw json.RawMessage) (string, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return "", nil
	}
	var shape struct {
		Properties map[string]struct {
			Description string `json:"description"`
			Title       string `json:"title"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &shape); err != nil {
		return "", fmt.Errorf("解析 inputSchema: %w", err)
	}
	if len(shape.Properties) == 0 {
		return "", nil
	}

	keys := make([]string, 0, len(shape.Properties))
	for k := range shape.Properties {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	units := make([]string, 0, len(keys))
	for _, k := range keys {
		p := shape.Properties[k]
		note := strings.TrimSpace(p.Description)
		if note == "" {
			note = strings.TrimSpace(p.Title)
		}
		note = clipNote(strings.Join(strings.Fields(note), " "), MaxParamNote)
		if note == "" {
			units = append(units, k)
			continue
		}
		units = append(units, k+"（"+note+"）")
	}

	// 按"完整单元"截断：超限就停在单元边界上，避免留下半个括号或半句话。
	const sep = " · "
	sepLen := utf8.RuneCountInString(sep)
	var b strings.Builder
	total := 0
	for _, u := range units {
		n := utf8.RuneCountInString(u)
		if total > 0 {
			n += sepLen
		}
		if total+n > MaxSchemaHints {
			break
		}
		if total > 0 {
			b.WriteString(sep)
		}
		b.WriteString(u)
		total += n
	}
	return b.String(), nil
}

// Text 按勾选字段组装语料文本，段序固定：工具名 → 描述 → 参数线索 →
// 标签（排序后空格连接）→ server 描述；空段跳过，全空返回空串（不参与 embed）。
// 输出必须确定：同一输入逐字节一致，语料哈希才不会把增量重嵌放大成全量重嵌。
func Text(toolName, toolDescription string, tags []string, serverDesc, schemaHints string, f Fields) string {
	var parts []string
	if f.ToolName {
		if s := NaturalName(toolName); s != "" {
			parts = append(parts, s)
		}
	}
	if f.ToolDescription {
		if s := clipRunes(strings.TrimSpace(toolDescription), MaxToolDescription); s != "" {
			parts = append(parts, s)
		}
	}
	if f.SchemaHints {
		if s := strings.TrimSpace(schemaHints); s != "" {
			parts = append(parts, s)
		}
	}
	if f.Tags && len(tags) > 0 {
		sorted := append([]string(nil), tags...)
		sort.Strings(sorted)
		parts = append(parts, strings.Join(sorted, " "))
	}
	if f.ServerDesc {
		if s := clipRunes(strings.TrimSpace(serverDesc), MaxServerDesc); s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, "\n")
}

// Hash 是语料的增量判定指纹。刻意不含 model 与 dims：那是"整代重嵌"，
// 由代身份 (model, dims, spec) 表达，哈希只回答"这段语料本身变没变"。
func Hash(text string) string {
	h := fnv.New64a()
	// hash.Hash.Write 按接口约定永不返回错误（文档明示），两个返回值都无需检查。
	_, _ = h.Write([]byte(text))
	return hex.EncodeToString(h.Sum(nil))
}

// clipRunes 按字符（而非字节）截断，避免把一个 UTF-8 汉字切成半个。
// 只遍历到第 n 个字符为止，不把整串转成 []rune。
// 前置条件 n > 0：非正数只能是改错了代码，所以直接 panic，不做静默降级。
func clipRunes(s string, n int) string {
	if n <= 0 {
		panic("corpus.clipRunes: n 必须为正")
	}
	i := 0
	for idx := range s {
		if i == n {
			return strings.TrimSpace(s[:idx])
		}
		i++
	}
	return s
}

// clipNote 截断参数说明，超长时以省略号收尾，保证外层的 "（…）" 始终配平。
// 结果可能比 n 多一个省略号字符；调用方按实际长度计数即可。
func clipNote(s string, n int) string {
	if n <= 0 {
		panic("corpus.clipNote: n 必须为正")
	}
	i := 0
	for idx := range s {
		if i == n {
			return strings.TrimSpace(s[:idx]) + "…"
		}
		i++
	}
	return s
}
