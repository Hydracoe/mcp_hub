// Command mcp-hub-desktop 是桌面形态入口：自带引擎与存储，本机即完整网关。
//
// 与 cmd/mcp-hub（命令行 / 服务端形态）的区别只有三处，全部在 main 里：
//
//	数据放哪   应用数据目录（~/Library/Application Support/mcp-hub 等）
//	监听地址   默认 127.0.0.1:9090（固定，好让 AI 客户端写死配置）
//	谁来驱动   关窗 / Ctrl-C 即停，没有"以后台服务身份常驻"这回事
//
// 其余一切——数据库/缓存后端选择、迁移、探活、向量重建、优雅退出——
// 都走 internal/engine，与命令行入口是同一份实现。这是刻意的：
// 两条部署路线共享业务，差异只允许存在于"装配参数"这一层。
//
// # 壳有两种，构建时二选一
//
//	go build ./cmd/mcp-hub-desktop              打开系统默认浏览器（无 cgo）
//	go build -tags wails ./cmd/mcp-hub-desktop   Wails v3 原生窗口
//
// 后者需要 cgo（macOS 走 Cocoa/WebKit，Linux 走 GTK），前者不需要、
// 因此可交叉编译。两者的业务行为完全一致，差别只在"界面画在哪"：
// Wails 壳把后端直接挂进 webview 的资源服务器，界面因此不经过 TCP
// 端口；浏览器壳则真的访问 127.0.0.1 上那个地址。见 shell.go。
package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/hydra/mcp-hub/internal/config"
	"github.com/hydra/mcp-hub/internal/engine"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "启动失败: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	dataDir, err := ensureDataDir()
	if err != nil {
		return err
	}
	logger := newLogger()

	// 配置文件的位置也属于"桌面形态"：放应用数据目录，而不是当前工作目录
	// （双击启动时工作目录是 / 或 System32，那边的 .env 不该被读到）。
	// 显式路径不存在时 LoadFileFrom 会按默认顺序回退，所以命令行用法不受影响。
	if cf, err := config.LoadFileFrom(filepath.Join(dataDir, ".env")); err != nil {
		return err
	} else if cf.Path != "" {
		logger.Info("已载入配置", zap.String("path", cf.Path), zap.Int("items", cf.Loaded))
		for _, name := range cf.Unknown {
			logger.Warn("配置项不认识，已忽略", zap.String("name", name))
		}
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	for _, w := range cfg.Warnings {
		logger.Warn("配置可疑", zap.String("detail", w))
	}

	eng, err := engine.New(engine.Options{
		Settings: cfg,
		Logger:   logger,
		DataDir:  dataDir,
		Addr:     desktopAddr(),
	})
	if err != nil {
		return err
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), engine.ShutdownTimeout)
		defer cancel()
		if err := eng.Stop(ctx); err != nil {
			logger.Error("关闭失败", zap.Error(err))
		}
	}()

	info, err := eng.Start()
	if err != nil {
		return err
	}

	// 关窗 / 退出信号：桌面形态的"生命周期终点"。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	shell := newShell(logger)
	if err := shell.Run(ctx, eng, info); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	logger.Info("开始退出")
	return nil
}

// desktopAddr 返回桌面形态的监听地址。
//
// 优先尊重用户显式配置的 MCPHUB_ADDR（读的是"用户实际写了什么"，
// config.String 不填默认值，所以"没配"与"配成 :9090"能分辨开）；
// 没配则用固定的 127.0.0.1:9090。
//
// 为什么是固定端口而不是 :0——初版用的是 :0，理由是"不占固定端口、
// 也不会因为被占用而起不来"。接上 Wails 之后这个理由不再成立：
//
//   - 界面已经不走这个端口了（webview 的请求在进程内直接打到 gin），
//     所以端口冲突不再影响界面能不能打开；
//   - 于是端口的唯一用途就是**给外部 AI 客户端连**，而一个每次启动
//     都变的地址等于客户端永远配不对——桌面端的核心价值就没了。
//
// 9090 被占用时退回系统分配。这一步看着像"静默降级"，其实不是：
// 界面照常打开、用户还能进设置改端口，真实地址写在日志与窗口标题里。
// 让一个 GUI 应用因为端口冲突直接起不来，才是更糟的选择。
func desktopAddr() string {
	if addr := config.String(config.EnvAddr); addr != "" {
		return addr
	}
	const preferred = "127.0.0.1:9090"
	if !addrAvailable(preferred) {
		return "127.0.0.1:0"
	}
	return preferred
}

// addrAvailable 探测地址此刻能否绑定。
//
// 与 engine.Start 里的那次绑定之间有一个极短的时间窗，理论上可能被
// 别的进程抢走——抢走了 engine.Start 会如实报错，不会带着错地址继续跑。
// 为了这个窗口引入"重试到 :0"的复杂逻辑不划算，如实失败已经够好。
func addrAvailable(addr string) bool {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}

// ensureDataDir 定位并创建应用数据目录。
//
// 用 os.UserConfigDir 而不是写死各平台路径：
//   - macOS   ~/Library/Application Support
//   - Windows %AppData%
//   - Linux   $XDG_CONFIG_HOME 或 ~/.config
//
// 库文件（mcp-hub.db）、配置（.env）、日志都落在它下面。
func ensureDataDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		// 拿不到系统目录不是致命错：退到可执行文件旁边，
		// 至少数据还在，用户能找到。
		exe, exeErr := os.Executable()
		if exeErr != nil {
			return "", fmt.Errorf("定位应用数据目录失败: %w", err)
		}
		base = filepath.Dir(exe)
	}
	dir := filepath.Join(base, "mcp-hub")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("创建应用数据目录 %s 失败: %w", dir, err)
	}
	return dir, nil
}

func newLogger() *zap.Logger {
	cfg := zap.NewProductionConfig()
	cfg.Level = zap.NewAtomicLevelAt(zapcore.InfoLevel)
	// 桌面形态没有控制台给人看日志，写进文件比写到 stderr 有用。
	if dir, err := os.UserConfigDir(); err == nil {
		cfg.OutputPaths = []string{filepath.Join(dir, "mcp-hub", "desktop.log"), "stderr"}
	}
	logger, err := cfg.Build()
	if err != nil {
		return zap.NewNop()
	}
	return logger
}
