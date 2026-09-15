package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/hydra/mcp-hub/internal/downstream"
	"github.com/hydra/mcp-hub/internal/store"
)

// runAdd 添加一个下游 server，并顺手探活一次把工具清单拉下来。
//
// 探活不是可选的额外步骤而是默认行为：一个加了却从没连通的 server,
// 在管理界面里和一个配置正确的 server 长得一模一样，直到 AI 调用时才暴露。
func runAdd(args []string) int {
	fs := flag.NewFlagSet("add", flag.ExitOnError)

	var (
		key         = fs.String("key", "", "工具前缀（留空则从 url/command 推断）")
		name        = fs.String("name", "", "显示名（留空同 key）")
		urlFlag     = fs.String("url", "", "MCP 端点 URL")
		transport   = fs.String("transport", "", "streamable_http | sse | stdio（默认按参数推断）")
		command     = fs.String("command", "", "stdio 可执行文件")
		timeout     = fs.Duration("timeout", downstream.DefaultTimeout, "请求超时")
		legacyOnly  = fs.Bool("legacy-only", false, "强制旧版协议握手")
		pinnedProto = fs.String("protocol-version", "", "固定协议版本，留空自动协商")
		disabled    = fs.Bool("disabled", false, "写入配置但先不启用")
		noProbe     = fs.Bool("no-probe", false, "只写配置，不探活")
		dbPath      = fs.String("db", "mcp-hub.db", "数据库：SQLite 文件路径，或 postgres:// 连接串")
	)
	var headers headerList
	fs.Var(&headers, "header", "HTTP 头，可重复，格式 \"Key: Value\"")
	var cmdArgs stringList
	fs.Var(&cmdArgs, "arg", "stdio 参数，可重复")
	var envs stringList
	fs.Var(&envs, "env", "stdio 环境变量，可重复，格式 \"KEY=VALUE\"")

	if err := fs.Parse(args); err != nil {
		return 2
	}

	spec, err := buildSpec(*urlFlag, *transport, *command, cmdArgs, headers, *timeout, *legacyOnly, *pinnedProto)
	if err != nil {
		fmt.Fprintf(os.Stderr, "配置错误: %v\n", err)
		return 2
	}
	if *key != "" {
		spec.Key = *key
	}
	if *name != "" {
		spec.Name = *name
	} else if spec.Name == "" {
		spec.Name = spec.Key
	}
	spec.Env = envs

	if err := spec.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "配置错误: %v\n", err)
		return 2
	}

	st, err := store.Open(*dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "打开数据库失败: %v\n", err)
		return 3
	}
	defer st.Close()

	ctx := context.Background()
	if err := st.UpsertServer(ctx, store.Config{Spec: spec, Enabled: !*disabled}); err != nil {
		fmt.Fprintf(os.Stderr, "写入配置失败: %v\n", err)
		return 3
	}

	saved, err := st.GetServerByKey(ctx, spec.Key)
	if err != nil {
		fmt.Fprintf(os.Stderr, "回读配置失败: %v\n", err)
		return 3
	}

	if *noProbe {
		fmt.Printf("已添加 %s（未探活）\n", spec.Key)
		return 0
	}

	probeCtx, cancel := context.WithTimeout(ctx, spec.TimeoutOr()+5*time.Second)
	defer cancel()

	res := downstream.NewProber(spec).Probe(probeCtx)
	if err := st.SaveProbe(ctx, saved.Spec.ID, res); err != nil {
		fmt.Fprintf(os.Stderr, "写入探活结果失败: %v\n", err)
		return 3
	}

	printResult(spec, res)
	if !res.OK {
		// 配置已经写进去了——用户多半是想先看看哪儿不对，
		// 而不是让这次尝试什么都不留下。
		fmt.Fprintf(os.Stderr, "\n配置已保存，但探活失败。用 mcp-hub probe 重试或修正后再次 add。\n")
		return 1
	}
	return 0
}
