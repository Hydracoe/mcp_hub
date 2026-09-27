//go:build !wails

package main

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"time"

	"go.uber.org/zap"

	"github.com/hydra/mcp-hub/internal/engine"
)

func newShell(log *zap.Logger) Shell { return &browserShell{log: log} }

// browserShell 是默认的桌面实现：引擎已经起在回环地址上，
// 把系统默认浏览器指过去。
//
// 它不引入任何 GUI 依赖、不需要 cgo、三平台都能交叉编译，也让
// "引擎 + 前端"这条链路在没有 GUI 工具链的机器上（CI、容器、
// 交叉编译的目标机）依然可被端到端验证。要原生窗口就加 -tags wails。
type browserShell struct {
	log *zap.Logger
}

func (s *browserShell) Run(ctx context.Context, eng *engine.Engine, info engine.Info) error {
	shellReady(s.log, info, "browser")
	printReady(info)

	if err := openBrowser(info.BaseURL); err != nil {
		// 打不开浏览器不是致命错：地址已经打出来了，手动点也能用。
		s.log.Warn("调用系统浏览器失败，请手动打开上面的地址",
			zap.String("url", info.BaseURL), zap.Error(err))
	}

	// 引擎自己挂了要让窗口/进程知道，否则用户面对的是一个
	// "进程还在但什么都不响应"的黑洞。
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-eng.Errors():
		if err != nil {
			return fmt.Errorf("引擎异常退出: %w", err)
		}
		return nil
	}
}

func printReady(info engine.Info) {
	fmt.Printf("\n  MCP Hub 桌面端已就绪\n\n")
	fmt.Printf("  管理界面   %s\n", info.BaseURL)
	fmt.Printf("  MCP 端点   %s\n", info.MCPURL)
	fmt.Printf("\n  数据库     %s（%s）\n", info.DBDialect, info.DBLocator)
	fmt.Printf("  缓存       %s\n", info.CacheDialect)
	fmt.Printf("\n  按 Ctrl+C 退出\n\n")
}

// openBrowser 用系统默认浏览器打开地址。
// 三种平台各有一条命令，都不经过 shell 拼接——URL 由本进程生成，
// 但"不把字符串交给 shell 解释"这条规矩没有例外。
func openBrowser(url string) error {
	cmd, args := browserCommand(url)
	if cmd == "" {
		return fmt.Errorf("不支持的平台 %s", runtime.GOOS)
	}
	c := exec.Command(cmd, args...)
	if err := c.Start(); err != nil {
		return err
	}
	// 不等它退出：浏览器是长驻进程，等下去会把启动流程挂死。
	// 但也不能直接丢着——起一个短命 goroutine 收尸，避免僵尸进程。
	go func() {
		_ = c.Wait()
	}()
	// 给浏览器一点启动时间，让下面的日志不至于排在它前面显得错乱。
	time.Sleep(200 * time.Millisecond)
	return nil
}

func browserCommand(url string) (string, []string) {
	switch runtime.GOOS {
	case "darwin":
		return "open", []string{url}
	case "windows":
		// rundll32 是 Windows 上无 shell 打开 URL 的既定做法：
		// `cmd /c start` 会把 URL 里的 & 交给 cmd 解释。
		return "rundll32", []string{"url.dll,FileProtocolHandler", url}
	default:
		return "xdg-open", []string{url}
	}
}
