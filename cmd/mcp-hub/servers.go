package main

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/hydra/mcp-hub/internal/downstream"
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
	cmd.Flags().StringVar(&dbPath, "db", "", dbFlagUsage)
	return cmd
}

func runServers(dbPath string) int {
	st, err := openStore(dbPath)
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
