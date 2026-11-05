// Command mcp-hub-desktop 是桌面形态入口：自带引擎与存储，本机即完整网关。
//
// 与 cmd/mcp-hub 的差异只在装配参数：数据放系统应用数据目录，默认监听
// 127.0.0.1:9090（固定，便于 AI 客户端写死配置），生命周期随关窗/信号结束。
//
// 壳按构建标签二选一：默认打开系统默认浏览器（无 cgo，可交叉编译）；
// -tags wails 用 Wails v3 原生窗口（需 cgo，界面走进程内 handler，不经过端口）。
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

	// 配置放应用数据目录而非工作目录：双击启动时工作目录不可靠。
	// 显式路径不存在时 LoadFileFrom 会按默认顺序回退。
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
// 优先用用户显式配置的 MCPHUB_ADDR（config.String 不填默认值，能区分
// "没配"与"配成 :9090"）；否则用固定的 127.0.0.1:9090。
//
// 固定端口是为了让外部 AI 客户端能写死配置；被占用时退回 :0，
// 界面照常打开，真实地址写在日志与窗口标题里。
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
// 与 engine.Start 的绑定之间有极短时间窗；真被抢走时 engine.Start 会如实报错。
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
// 用 os.UserConfigDir：macOS ~/Library/Application Support、Windows %AppData%、
// Linux $XDG_CONFIG_HOME 或 ~/.config。库文件、.env、日志都落在它下面。
func ensureDataDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		// 拿不到系统目录不是致命错：退到可执行文件旁边，至少数据还在。
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
