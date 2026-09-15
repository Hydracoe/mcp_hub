package search

import (
	"fmt"
	"hash/fnv"
	"sort"
	"strings"
)

// CorpusFields 勾选进向量化语料的字段（PRD §4.7.5，默认：工具描述 + server 描述）。
type CorpusFields struct {
	ToolDescription bool `json:"toolDescription"`
	Tags            bool `json:"tags"`
	ServerDesc      bool `json:"serverDescription"`
}

// DefaultCorpusFields 是 PRD 拍板的默认勾选。
func DefaultCorpusFields() CorpusFields {
	return CorpusFields{ToolDescription: true, ServerDesc: true}
}

// CorpusText 按勾选字段组装语料文本。非空字段以换行分隔，tags 以空格连接；
// 全空返回空串（空串不应参与 embed）。
func CorpusText(toolDescription string, tags []string, serverDesc string, f CorpusFields) string {
	var parts []string
	if f.ToolDescription {
		if s := strings.TrimSpace(toolDescription); s != "" {
			parts = append(parts, s)
		}
	}
	if f.Tags && len(tags) > 0 {
		sorted := append([]string(nil), tags...)
		sort.Strings(sorted)
		parts = append(parts, strings.Join(sorted, " "))
	}
	if f.ServerDesc {
		if s := strings.TrimSpace(serverDesc); s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, "\n")
}

// CorpusHash 是语料的增量判定指纹：hash(语料文本 + model + dims)。
// 三者任一变化 → hash 变化 → 需要重新 embed（PRD §4.7.5）。
func CorpusHash(text, model string, dims int) string {
	h := fnv.New64a()
	fmt.Fprintf(h, "%s\x00%s\x00%d", text, model, dims)
	return fmt.Sprintf("%016x", h.Sum64())
}
