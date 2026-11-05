package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/hydra/mcp-hub/internal/downstream"
	"github.com/hydra/mcp-hub/internal/store/servers"
)

type probeOptions struct {
	url             string
	transport       string
	command         string
	protocolVersion string
	db              string
	timeout         time.Duration
	legacyOnly      bool
	asJSON          bool
	headers         headerList
	args            []string
}

func newProbeCmd() *cobra.Command {
	var o probeOptions
	cmd := &cobra.Command{
		Use:   "probe",
		Short: "探活一个下游 MCP server (L1–L3)，不落库",
		Long: `探活一个下游 MCP server (L1–L3)，不落库（加 --db 可落库）。

  mcp-hub probe --url http://127.0.0.1:3000/mcp
  mcp-hub probe --url http://host/mcp --header "Authorization: Bearer xxx" --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return codeToErr(runProbe(o))
		},
	}
	fs := cmd.Flags()
	fs.StringVar(&o.url, "url", "", "MCP 端点 URL")
	fs.StringVar(&o.transport, "transport", "", "streamable_http | sse | stdio（默认按参数推断）")
	fs.DurationVar(&o.timeout, "timeout", downstream.DefaultTimeout, "探活超时")
	fs.BoolVar(&o.legacyOnly, "legacy-only", false, "强制旧版协议握手")
	fs.StringVar(&o.protocolVersion, "protocol-version", "", "固定协议版本，留空自动协商")
	fs.BoolVar(&o.asJSON, "json", false, "输出原始 JSON")
	fs.StringVar(&o.command, "command", "", "stdio 可执行文件")
	fs.StringVar(&o.db, "db", "", dbFlagUsage+"；指定则把探活结果落库")
	fs.Var(&o.headers, "header", `HTTP 头，可重复，格式 "Key: Value"`)
	fs.StringArrayVar(&o.args, "arg", nil, "stdio 参数，可重复")
	return cmd
}

func runProbe(o probeOptions) int {
	spec, err := buildSpec(o.url, o.transport, o.command, o.args, o.headers,
		o.timeout, o.legacyOnly, o.protocolVersion)
	if err != nil {
		fmt.Fprintf(os.Stderr, "配置错误: %v\n", err)
		return 2
	}

	ctx, cancel := context.WithTimeout(context.Background(), spec.TimeoutOr()+5*time.Second)
	defer cancel()

	result := downstream.NewProber(spec).Probe(ctx)

	if o.db != "" {
		if err := persistResult(o.db, spec, result); err != nil {
			fmt.Fprintf(os.Stderr, "落库失败: %v\n", err)
			return 3
		}
	}

	if o.asJSON {
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

// persistResult 把探活结果连同配置写进数据库。
func persistResult(dbPath string, spec downstream.Spec, res *downstream.ProbeResult) error {
	st, err := openStore(dbPath)
	if err != nil {
		return err
	}
	defer st.Close()

	ctx := context.Background()
	if err := st.Servers.Upsert(ctx, servers.Config{Spec: spec, Enabled: true}); err != nil {
		return err
	}
	srv, err := st.Servers.GetByKey(ctx, spec.Key)
	if err != nil {
		return err
	}
	return st.Servers.SaveProbe(ctx, srv.Spec.ID, res)
}
