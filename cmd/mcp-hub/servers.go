package main

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/hydra/mcp-hub/internal/downstream"
	"github.com/hydra/mcp-hub/internal/store"
)

func newServersCmd() *cobra.Command {
	var dbPath string
	cmd := &cobra.Command{
		Use:   "servers",
		Short: "列出已配置的 server",
		RunE: func(cmd *cobra.Command, args []string) error {
			return codeToErr(runServers(dbPath))
		},
	}
	cmd.Flags().StringVar(&dbPath, "db", "", "数据库：postgres:// 连接串；缺省读配置项 MCPHUB_DB（.env 或进程环境变量）")
	return cmd
}

func runServers(dbPath string) int {
	if dbPath == "" {
		dbPath = StringConfig(envDB)
	}
	if dbPath == "" {
		fmt.Fprintln(os.Stderr, "需要数据库：设 MCPHUB_DB，或用 --db 指定")
		return 2
	}

	st, err := store.Open(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "打开数据库失败: %v\n", err)
		return 3
	}
	defer st.Close()

	ctx := context.Background()
	servers, err := st.Servers.List(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "查询失败: %v\n", err)
		return 3
	}
	if len(servers) == 0 {
		fmt.Println("（尚未配置任何 server）")
		return 0
	}

	tools, _ := st.Servers.ListTools(ctx)
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
