package main

import (
	"fmt"
	"strings"

	"github.com/hydra/mcp-hub/internal/downstream"
)

// printResult 以人类可读格式打印探活结果（add / probe 共用）。
func printResult(spec downstream.Spec, r *downstream.ProbeResult) {
	if !r.OK {
		fmt.Printf("探活失败  %s (%s)\n\n", spec.Name, spec.Transport)
		fmt.Printf("  阶段      %s\n", r.Stage)
		fmt.Printf("  分类      %s\n", r.Kind)
		fmt.Printf("  原因      %s\n", r.Kind.Human())
		if r.Detail != "" {
			fmt.Printf("  详情      %s\n", r.Detail)
		}
		if r.Stderr != "" {
			fmt.Printf("  stderr    %s\n", indentBlock(r.Stderr))
		}
		fmt.Printf("  耗时      %s\n", formatLatency(r.Latency))
		return
	}

	era := "modern"
	if r.Legacy {
		era = "legacy"
	}

	fmt.Printf("探活成功  %s (%s)\n\n", spec.Name, spec.Transport)
	fmt.Printf("  服务      %s %s\n", r.ServerName, r.ServerVersion)
	fmt.Printf("  协议      %s  [%s]\n", r.ProtocolVersion, era)
	if len(r.Capabilities) > 0 {
		fmt.Printf("  能力      %s\n", strings.Join(r.Capabilities, ", "))
	}
	fmt.Printf("  工具数    %d\n", r.ToolCount)
	fmt.Printf("  耗时      %s\n", formatLatency(r.Latency))

	if len(r.Tools) > 0 {
		fmt.Printf("\n工具列表（对外暴露名）:\n")
		for _, tool := range r.Tools {
			fmt.Printf("  - %s__%s\n", spec.Key, tool.Name)
		}
	}
}

func formatLatency(l downstream.StageLatency) string {
	parts := make([]string, 0, 5)
	if l.Network > 0 {
		parts = append(parts, fmt.Sprintf("网络 %dms", l.Network))
	}
	if l.Connect > 0 {
		parts = append(parts, fmt.Sprintf("连接 %dms", l.Connect))
	}
	if l.Discover > 0 {
		parts = append(parts, fmt.Sprintf("协商 %dms", l.Discover))
	}
	if l.ListTools > 0 {
		parts = append(parts, fmt.Sprintf("列表 %dms", l.ListTools))
	}
	parts = append(parts, fmt.Sprintf("合计 %dms", l.Total))
	return strings.Join(parts, " · ")
}

func indentBlock(s string) string {
	return strings.ReplaceAll(strings.TrimSpace(s), "\n", "\n            ")
}
