package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/hydra/mcp-hub/internal/downstream"
)

// newRootCmd 组装命令树并注册全部子命令。
func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "mcp-hub",
		Short: "MCP 聚合网关",
		Long: `mcp-hub — MCP 聚合网关

serve 的配置全部来自 .env 与进程环境变量，不接受任何命令行参数：

  mcp-hub env                                  # 先看有哪些可配的
  mcp-hub serve                                # 从 ./.env 读配置
  MCPHUB_DB='postgres://…' mcp-hub serve       # 一次性覆盖某一项

  查找顺序：$MCPHUB_ENV_FILE → ./.env → 可执行文件旁的 .env，
  只加载第一个命中的文件。进程环境变量**优先于** .env，因此
  MCPHUB_ADDR=:8080 mcp-hub serve 这类一次性覆盖，以及容器 / K8s
  注入的值，都不会被一个陈旧的 .env 吃掉。

  表结构在首次连接时自动同步（需要先建好空库）。
  未配置 MCPHUB_EMBED_API_KEY 时检索自动降级 keyword/bm25，网关照常运行。

add / probe / servers 的其余参数用 -h 查看。`,
		Example: `  add 远程 server（--db 可省略，缺省读 MCPHUB_DB）:
  mcp-hub add --url https://mcp.deepwiki.com/mcp
  mcp-hub add --key github --url http://host/mcp \
      --header "Authorization: Bearer xxx"

  add stdio server:
  mcp-hub add --key everything \
      --command npx --arg -y --arg @modelcontextprotocol/server-everything

  probe（只测不存）:
  mcp-hub probe --url http://127.0.0.1:3000/mcp
  mcp-hub probe --url http://host/mcp --header "Authorization: Bearer xxx" --json`,
		// 错误输出和退出码都由 main 统一处理，cobra 不自己打印。
		SilenceErrors: true,
		SilenceUsage:  true,
		Args:          cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// 帮助打到 stderr，stdout 可能正被别的程序读。
			if len(args) > 0 {
				fmt.Fprintf(cmd.ErrOrStderr(), "unknown command %q\n\n", args[0])
			}
			cmd.SetOut(cmd.ErrOrStderr())
			_ = cmd.Help()
			return &exitError{code: 2}
		},
	}
	// 坏 flag 时把用法也打出来，退出码 2。
	root.SetFlagErrorFunc(func(c *cobra.Command, err error) error {
		fmt.Fprintln(c.ErrOrStderr(), err)
		fmt.Fprint(c.ErrOrStderr(), c.UsageString())
		return &exitError{code: 2}
	})

	root.AddCommand(
		newServeCmd(),
		newAddCmd(),
		newProbeCmd(),
		newServersCmd(),
		newEnvCmd(),
		newVersionCmd(),
	)
	return root
}

func newEnvCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "env",
		Aliases: []string{"config"},
		Short:   "打印全部配置项（变量名 / 默认值 / 说明）",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			PrintEnvHelp(os.Stdout)
			return nil
		},
	}
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "打印版本",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprintf(cmd.OutOrStdout(), "%s %s\n", downstream.ClientName, downstream.ClientVersion)
			return nil
		},
	}
}
