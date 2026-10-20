package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/hydra/mcp-hub/internal/downstream"
	"github.com/hydra/mcp-hub/internal/store/servers"
	"github.com/hydra/mcp-hub/internal/store/storeutil"
)

type addOptions struct {
	key             string
	name            string
	url             string
	transport       string
	command         string
	timeout         time.Duration
	legacyOnly      bool
	protocolVersion string
	disabled        bool
	noProbe         bool
	category        string
	db              string
	headers         headerList
	args            []string
	envs            []string
}

func newAddCmd() *cobra.Command {
	var o addOptions
	cmd := &cobra.Command{
		Use:   "add",
		Short: "添加一个下游 server（会顺带探活一次）",
		Long: `添加一个下游 server，并顺手探活一次把工具清单拉下来。

  mcp-hub add --url https://mcp.deepwiki.com/mcp
  mcp-hub add --key github --url http://host/mcp \
      --header "Authorization: Bearer xxx"
  mcp-hub add --key everything \
      --command npx --arg -y --arg @modelcontextprotocol/server-everything

新增 server 必须用 --category 指定分类；更新已有 server 时省略
--category 则保留原分类。`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return codeToErr(runAdd(o))
		},
	}
	fs := cmd.Flags()
	fs.StringVar(&o.key, "key", "", "工具前缀（留空则从 url/command 推断）")
	fs.StringVar(&o.name, "name", "", "显示名（留空同 key）")
	fs.StringVar(&o.url, "url", "", "MCP 端点 URL")
	fs.StringVar(&o.transport, "transport", "", "streamable_http | sse | stdio（默认按参数推断）")
	fs.StringVar(&o.command, "command", "", "stdio 可执行文件")
	fs.DurationVar(&o.timeout, "timeout", downstream.DefaultTimeout, "请求超时")
	fs.BoolVar(&o.legacyOnly, "legacy-only", false, "强制旧版协议握手")
	fs.StringVar(&o.protocolVersion, "protocol-version", "", "固定协议版本，留空自动协商")
	fs.BoolVar(&o.disabled, "disabled", false, "写入配置但先不启用")
	fs.BoolVar(&o.noProbe, "no-probe", false, "只写配置，不探活")
	fs.StringVar(&o.category, "category", "", "分类 id（新增必填，见 list-categories）")
	fs.StringVar(&o.db, "db", "", dbFlagUsage)
	fs.Var(&o.headers, "header", `HTTP 头，可重复，格式 "Key: Value"`)
	fs.StringArrayVar(&o.args, "arg", nil, "stdio 参数，可重复")
	fs.StringArrayVar(&o.envs, "env", nil, `stdio 环境变量，可重复，格式 "KEY=VALUE"`)
	return cmd
}

func runAdd(o addOptions) int {
	spec, err := buildSpec(o.url, o.transport, o.command, o.args, o.headers,
		o.timeout, o.legacyOnly, o.protocolVersion)
	if err != nil {
		fmt.Fprintf(os.Stderr, "配置错误: %v\n", err)
		return 2
	}
	if o.key != "" {
		spec.Key = o.key
	}
	if o.name != "" {
		spec.Name = o.name
	} else {
		// --name 留空时显示名同 key（帮助文案如此承诺）。buildSpec 里的
		// 推断名是在 --key 覆盖之前算的，必须以最终的 key 为准，
		// 否则名字与用户指定的前缀对不上。
		spec.Name = spec.Key
	}
	spec.Env = o.envs

	if err := spec.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "配置错误: %v\n", err)
		return 2
	}
	st, err := openStore(o.db)
	if err != nil {
		fmt.Fprintf(os.Stderr, "打开数据库失败: %v\n", err)
		return 3
	}
	defer st.Close()

	ctx := context.Background()

	cfg := servers.Config{Spec: spec, Enabled: !o.disabled, CategoryID: o.category}
	if prev, err := st.Servers.GetByKey(ctx, spec.Key); err == nil {
		if o.category == "" {
			cfg.CategoryID = prev.CategoryID
		}
	} else if errors.Is(err, storeutil.ErrNotFound) && o.category == "" {
		fmt.Fprintln(os.Stderr, "新增 server 必须用 --category 指定分类（可用 list-categories 查看分类 id）")
		return 2
	}

	// 分类必须真实存在：写进去一个不存在的 id 就是悬空引用，界面上
	// 那一列会变成空白分类。与管理接口的校验同一把尺。
	if cfg.CategoryID != "" {
		if _, err := st.Categories.Get(ctx, cfg.CategoryID); err != nil {
			fmt.Fprintf(os.Stderr, "分类 %q 不存在（可用 list-categories 查看分类 id）\n", cfg.CategoryID)
			return 2
		}
	}

	if err := st.Servers.Upsert(ctx, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "写入配置失败: %v\n", err)
		return 3
	}

	saved, err := st.Servers.GetByKey(ctx, spec.Key)
	if err != nil {
		fmt.Fprintf(os.Stderr, "回读配置失败: %v\n", err)
		return 3
	}

	if o.noProbe {
		fmt.Printf("已添加 %s（未探活）\n", spec.Key)
		return 0
	}

	probeCtx, cancel := context.WithTimeout(ctx, spec.TimeoutOr()+5*time.Second)
	defer cancel()

	res := downstream.NewProber(spec).Probe(probeCtx)
	if err := st.Servers.SaveProbe(ctx, saved.Spec.ID, res); err != nil {
		fmt.Fprintf(os.Stderr, "写入探活结果失败: %v\n", err)
		return 3
	}

	printResult(spec, res)
	if !res.OK {
		fmt.Fprintf(os.Stderr, "\n配置已保存，但探活失败。用 mcp-hub probe 重试或修正后再次 add。\n")
		return 1
	}
	return 0
}
