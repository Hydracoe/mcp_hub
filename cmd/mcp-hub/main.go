package main

import (
	"errors"
	"fmt"
	"os"
)

// main 只做三件事：加载配置文件、执行命令、统一处理退出码。
func main() {
	// 先读配置文件再解析命令。提示打在 stderr，
	// 保证 probe --json 的 stdout 是纯 JSON。
	if cf, err := loadConfigFile(); err != nil {
		fmt.Fprintf(os.Stderr, "加载配置失败: %v\n", err)
		os.Exit(2)
	} else if cf.Path != "" {
		fmt.Fprintf(os.Stderr, "已从 %s 载入 %d 个配置项\n", cf.Path, cf.Loaded)
		for _, name := range cf.Unknown {
			fmt.Fprintf(os.Stderr, "  不认识 %s，已忽略（mcp-hub env 可看到全部配置项）\n", name)
		}
	}

	if err := newRootCmd().Execute(); err != nil {
		var ee *exitError
		if errors.As(err, &ee) {
			if ee.err != nil {
				fmt.Fprintln(os.Stderr, ee.err)
			}
			os.Exit(ee.code)
		}
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(2)
	}
}

// exitError 把退出码从 cobra 带回 main：2=用法/配置错 3=环境错 1=探活失败。
// err 为空表示消息已经打过了。
type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string {
	if e.err == nil {
		return fmt.Sprintf("exit %d", e.code)
	}
	return e.err.Error()
}

func codeToErr(code int) error {
	if code == 0 {
		return nil
	}
	return &exitError{code: code}
}
