package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/hydra/mcp-hub/internal/downstream"
	"github.com/hydra/mcp-hub/internal/store"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	switch os.Args[1] {
	case "serve":
		os.Exit(runServe(os.Args[2:]))
	case "probe":
		os.Exit(runProbe(os.Args[2:]))
	case "servers":
		os.Exit(runServers(os.Args[2:]))
	case "add":
		os.Exit(runAdd(os.Args[2:]))
	case "version":
		fmt.Printf("%s %s\n", downstream.ClientName, downstream.ClientVersion)
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `mcp-hub — MCP 聚合网关

用法:
  mcp-hub serve [flags]     启动网关：聚合下游工具并对外提供 MCP 端点
  mcp-hub add [flags]       添加一个下游 server（会顺带探活一次）
  mcp-hub probe [flags]     探活一个下游 MCP server (L1–L3)，不落库
  mcp-hub servers --db F    列出已配置的 server
  mcp-hub version           打印版本

serve（最常用）:
  mcp-hub serve --db mcp-hub.db --addr :9090
  mcp-hub serve --addr 127.0.0.1:0                     # 端口由系统分配

数据库（--db）两种写法：
  --db mcp-hub.db                                      # SQLite，本地开发
  --db 'postgres://user:pw@127.0.0.1:5432/mcphub'      # PostgreSQL，生产形态
表结构在首次连接时自动同步（PostgreSQL 需要先建好空库）。

缓存（--cache）两种写法：
  --cache memory                                       # 进程内，默认
  --cache 'redis://127.0.0.1:6379/0'                   # Redis，生产形态

工具检索（PRD §4.7）：
  --list-mode meta | all                               # tools/list 返回什么，默认 meta
  --embed-base-url https://api.siliconflow.cn/v1       # embed 服务（OpenAI 兼容）
  --embed-api-key sk-xxx   # 缺省读环境变量 MCPHUB_EMBED_API_KEY
  --embed-model BAAI/bge-m3 --embed-dims 1024          # embed 模型与维度
  --rerank-model BAAI/bge-reranker-v2-m3               # 留空关闭 rerank
未配置 --embed-api-key 时检索自动降级 keyword/bm25，网关照常运行。

add 远程 server:
  mcp-hub add --db mcp-hub.db --url https://mcp.deepwiki.com/mcp
  mcp-hub add --db mcp-hub.db --key github --url http://host/mcp \
      --header "Authorization: Bearer xxx"

add stdio server:
  mcp-hub add --db mcp-hub.db --key everything \
      --command npx --arg -y --arg @modelcontextprotocol/server-everything

probe（只测不存）:
  mcp-hub probe --url http://127.0.0.1:3000/mcp
  mcp-hub probe --url http://host/mcp --header "Authorization: Bearer xxx" --json

flags:
`)
}

func runProbe(args []string) int {
	fs := flag.NewFlagSet("probe", flag.ExitOnError)

	var (
		urlFlag     = fs.String("url", "", "MCP 端点 URL")
		transport   = fs.String("transport", "", "streamable_http | sse | stdio（默认按参数推断）")
		timeout     = fs.Duration("timeout", downstream.DefaultTimeout, "探活超时")
		legacyOnly  = fs.Bool("legacy-only", false, "强制旧版协议握手")
		pinnedProto = fs.String("protocol-version", "", "固定协议版本，留空自动协商")
		asJSON      = fs.Bool("json", false, "输出原始 JSON")
		command     = fs.String("command", "", "stdio 可执行文件")
		dbPath      = fs.String("db", "", "数据库：SQLite 文件路径或 postgres:// 连接串；指定则把探活结果落库")
	)
	var headers headerList
	fs.Var(&headers, "header", "HTTP 头，可重复，格式 \"Key: Value\"")
	var cmdArgs stringList
	fs.Var(&cmdArgs, "arg", "stdio 参数，可重复")

	if err := fs.Parse(args); err != nil {
		return 2
	}

	spec, err := buildSpec(*urlFlag, *transport, *command, cmdArgs, headers, *timeout, *legacyOnly, *pinnedProto)
	if err != nil {
		fmt.Fprintf(os.Stderr, "配置错误: %v\n", err)
		return 2
	}

	ctx, cancel := context.WithTimeout(context.Background(), spec.TimeoutOr()+5*time.Second)
	defer cancel()

	result := downstream.NewProber(spec).Probe(ctx)

	if *dbPath != "" {
		if err := persistResult(*dbPath, spec, result); err != nil {
			fmt.Fprintf(os.Stderr, "落库失败: %v\n", err)
			return 3
		}
	}

	if *asJSON {
		out, _ := json.MarshalIndent(result, "", "  ")
		fmt.Println(string(out))
		if !result.OK {
			return 1
		}
		return 0
	}

	printResult(spec, result)
	if !result.OK {
		return 1
	}
	return 0
}

// buildSpec 把命令行参数折算成 Spec，并推断未显式给出的传输类型。
func buildSpec(
	rawURL, transport, command string,
	args []string,
	headers headerList,
	timeout time.Duration,
	legacyOnly bool,
	pinnedProto string,
) (downstream.Spec, error) {
	tr := downstream.Transport(transport)
	if tr == "" {
		switch {
		case command != "":
			tr = downstream.TransportStdio
		case rawURL != "":
			tr = downstream.TransportStreamableHTTP
		default:
			return downstream.Spec{}, fmt.Errorf("必须提供 --url 或 --command")
		}
	}

	spec := downstream.Spec{
		Key:             deriveKey(rawURL, command),
		Name:            deriveKey(rawURL, command),
		Transport:       tr,
		URL:             rawURL,
		Headers:         headers,
		Command:         command,
		Args:            args,
		Timeout:         timeout,
		LegacyOnly:      legacyOnly,
		ProtocolVersion: pinnedProto,
	}
	if err := spec.Validate(); err != nil {
		return downstream.Spec{}, err
	}
	return spec, nil
}

// deriveKey 从 URL 主机名或命令名推断工具前缀，仅用于展示。
func deriveKey(rawURL, command string) string {
	if command != "" {
		base := command
		if i := strings.LastIndex(base, "/"); i >= 0 {
			base = base[i+1:]
		}
		base = strings.TrimSuffix(base, ".exe")
		if base != "" {
			return sanitizeKey(base)
		}
	}
	if rawURL != "" {
		trimmed := strings.TrimPrefix(strings.TrimPrefix(rawURL, "https://"), "http://")
		host := trimmed
		if i := strings.IndexAny(host, "/:"); i >= 0 {
			host = host[:i]
		}
		if host != "" {
			return sanitizeKey(host)
		}
	}
	return "server"
}

func sanitizeKey(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + ('a' - 'A'))
		default:
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

// persistResult 把 server 配置与探活结果写入 SQLite。
func persistResult(dbPath string, spec downstream.Spec, res *downstream.ProbeResult) error {
	st, err := store.Open(dbPath)
	if err != nil {
		return err
	}
	defer st.Close()

	ctx := context.Background()
	if err := st.UpsertServer(ctx, store.Config{Spec: spec, Enabled: true}); err != nil {
		return err
	}
	srv, err := st.GetServerByKey(ctx, spec.Key)
	if err != nil {
		return err
	}
	return st.SaveProbe(ctx, srv.Spec.ID, res)
}

// runServers 打印已配置的 server 与最近一次探活状态。
func runServers(args []string) int {
	fs := flag.NewFlagSet("servers", flag.ExitOnError)
	dbPath := fs.String("db", "", "数据库：SQLite 文件路径，或 postgres:// 连接串")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dbPath == "" {
		fmt.Fprintln(os.Stderr, "servers 需要 --db 指定数据库")
		return 2
	}

	st, err := store.Open(*dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "打开数据库失败: %v\n", err)
		return 3
	}
	defer st.Close()

	ctx := context.Background()
	servers, err := st.ListServers(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "查询失败: %v\n", err)
		return 3
	}
	if len(servers) == 0 {
		fmt.Println("（尚未配置任何 server）")
		return 0
	}

	tools, _ := st.ListTools(ctx)
	toolCount := make(map[string]int, len(servers))
	for _, tl := range tools {
		toolCount[tl.ServerID]++
	}

	fmt.Printf("%-20s %-18s %-9s %-8s %-6s %-12s %s\n",
		"KEY", "TRANSPORT", "STATUS", "ENABLED", "TOOLS", "PROTOCOL", "TARGET")
	for _, s := range servers {
		target := s.Spec.URL
		if target == "" {
			target = s.Spec.Command
		}
		if s.LastErrorKind != downstream.KindNone {
			target = fmt.Sprintf("%s  [%s]", target, s.LastErrorKind)
		}
		enabled := "yes"
		if !s.Enabled {
			enabled = "no"
		}
		fmt.Printf("%-20s %-18s %-9s %-8s %-6d %-12s %s\n",
			s.Spec.Key, s.Spec.Transport, s.Status, enabled,
			toolCount[s.Spec.ID], s.Spec.ProtocolVersion, target)
	}
	return 0
}

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

// headerList 支持 -header "Key: Value" 重复出现。
type headerList map[string]string

func (h *headerList) String() string { return "" }

func (h *headerList) Set(v string) error {
	i := strings.Index(v, ":")
	if i < 0 {
		return fmt.Errorf("header 需要 \"Key: Value\" 格式，收到 %q", v)
	}
	if *h == nil {
		*h = map[string]string{}
	}
	(*h)[strings.TrimSpace(v[:i])] = strings.TrimSpace(v[i+1:])
	return nil
}

// stringList 支持 -arg 重复出现。
type stringList []string

func (s *stringList) String() string { return "" }

func (s *stringList) Set(v string) error {
	*s = append(*s, v)
	return nil
}
