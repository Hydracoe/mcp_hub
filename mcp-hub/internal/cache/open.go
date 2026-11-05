package cache

import (
	"context"
	"fmt"
	"strings"
)

// 支持的缓存后端名。与 config 包的取值一致——刻意的字符串常量重复：
// cache 是基础设施，不该 import 配置层。
const (
	DriverAuto   = "auto"
	DriverRedis  = "redis"
	DriverMemory = "memory"
)

// Options 是打开缓存后端的参数。
type Options struct {
	// Driver 是后端名：auto / redis / memory。
	// 留空等于 auto，按 Spec 的形态推断（redis:// 前缀即 redis，其余即 memory）。
	Driver string

	// Spec 是 Redis 连接串（redis:// 或 rediss://）。内存后端忽略本项。
	Spec string

	// Mem 是内存后端的可选参数。
	Mem MemoryOptions

	// Redis 是 Redis 后端的可选参数（连接串已由 Spec 给）。
	Redis RedisOptions
}

// Open 按后端名打开缓存。
// auto 只在形态明确时猜；认不出来的形态宁可报错，也不悄悄降级——
// “配了 Redis 却在用内存缓存”在运行时完全看不出来。
func Open(ctx context.Context, opts Options) (Store, error) {
	driver := strings.ToLower(strings.TrimSpace(opts.Driver))
	spec := strings.TrimSpace(opts.Spec)

	switch driver {
	case DriverMemory:
		// 显式选了内存后端：连接串本就该留空，有值也不影响（配置层已提醒）。
		return NewMemory(opts.Mem), nil
	case DriverRedis:
		if spec == "" {
			return nil, fmt.Errorf("缓存后端是 %s，但连接串为空", DriverRedis)
		}
		ro := opts.Redis
		ro.URL = spec
		return NewRedis(ctx, ro)
	case "", DriverAuto:
		switch {
		case spec == "":
			return NewMemory(opts.Mem), nil
		case isRedisSpec(spec):
			ro := opts.Redis
			ro.URL = spec
			return NewRedis(ctx, ro)
		default:
			return nil, fmt.Errorf("认不出缓存后端：连接串 %q 既不是空的（内存缓存）也不是 redis:// 形态", spec)
		}
	default:
		return nil, fmt.Errorf("不支持的缓存后端 %q，可选 %s | %s | %s", opts.Driver, DriverAuto, DriverRedis, DriverMemory)
	}
}

func isRedisSpec(spec string) bool {
	lower := strings.ToLower(spec)
	return strings.HasPrefix(lower, "redis://") || strings.HasPrefix(lower, "rediss://")
}
