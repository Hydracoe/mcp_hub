//go:build wails

package main

import (
	"context"
	"log/slog"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// zapSlogHandler 把 Wails 内部的 slog 日志接到我们的 zap 上。
//
// Wails 的 Options.Logger 不给就默认写 stderr；桌面双击启动时 stderr 无人可见，
// desktop.log 是资源加载失败与运行时错误的唯一现场。
type zapSlogHandler struct {
	log   *zap.Logger
	attrs []slog.Attr
	group string
}

func newZapSlogHandler(log *zap.Logger) *zapSlogHandler {
	// Named("wails") 让这些行一眼能与业务日志区分开，且便于单独过滤。
	return &zapSlogHandler{log: log.Named("wails")}
}

// Enabled 一律放行，层级判断交给 zap 自己做。
//
// 再判一次只会让两边级别定义漂移（zap 多一个 DPanic），过滤本来就在 zap core 里。
func (h *zapSlogHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *zapSlogHandler) Handle(_ context.Context, r slog.Record) error {
	fields := make([]zap.Field, 0, len(h.attrs)+r.NumAttrs())
	for _, a := range h.attrs {
		fields = appendSlogAttr(fields, h.group, a)
	}
	r.Attrs(func(a slog.Attr) bool {
		fields = appendSlogAttr(fields, h.group, a)
		return true
	})
	h.log.Log(slogToZapLevel(r.Level), r.Message, fields...)
	return nil
}

func (h *zapSlogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := *h
	// 复制而不是原地 append：WithAttrs 的调用方假定返回值与接收者互不影响。
	merged := make([]slog.Attr, 0, len(h.attrs)+len(attrs))
	merged = append(merged, h.attrs...)
	merged = append(merged, attrs...)
	next.attrs = merged
	return &next
}

func (h *zapSlogHandler) WithGroup(name string) slog.Handler {
	next := *h
	if name == "" {
		return &next
	}
	if h.group != "" {
		name = h.group + "." + name
	}
	next.group = name
	return &next
}

// appendSlogAttr 把一个 slog 属性折成 zap 字段，组名作为键前缀。
//
// 扁平化（"group.key"）而不是嵌套：日志键要能在 grep 里直接命中。
func appendSlogAttr(dst []zap.Field, prefix string, a slog.Attr) []zap.Field {
	if a.Value.Kind() == slog.KindLogValuer {
		a.Value = a.Value.Resolve()
	}

	key := a.Key
	if prefix != "" {
		key = prefix + "." + key
	}

	switch a.Value.Kind() {
	case slog.KindGroup:
		for _, sub := range a.Value.Group() {
			dst = appendSlogAttr(dst, key, sub)
		}
	case slog.KindTime:
		dst = append(dst, zap.Time(key, a.Value.Time()))
	case slog.KindDuration:
		dst = append(dst, zap.Duration(key, a.Value.Duration()))
	case slog.KindInt64:
		dst = append(dst, zap.Int64(key, a.Value.Int64()))
	case slog.KindUint64:
		dst = append(dst, zap.Uint64(key, a.Value.Uint64()))
	case slog.KindFloat64:
		dst = append(dst, zap.Float64(key, a.Value.Float64()))
	case slog.KindBool:
		dst = append(dst, zap.Bool(key, a.Value.Bool()))
	case slog.KindString:
		dst = append(dst, zap.String(key, a.Value.String()))
	default:
		dst = append(dst, zap.Any(key, a.Value.Any()))
	}
	return dst
}

func slogToZapLevel(l slog.Level) zapcore.Level {
	switch {
	case l < slog.LevelInfo:
		return zapcore.DebugLevel
	case l < slog.LevelWarn:
		return zapcore.InfoLevel
	case l < slog.LevelError:
		return zapcore.WarnLevel
	default:
		return zapcore.ErrorLevel
	}
}
