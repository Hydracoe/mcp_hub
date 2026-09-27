// Package config 是 mcp-hub 唯一的配置来源。
//
// 它刻意不依赖任何部署形态：命令行（cmd/mcp-hub）与桌面壳
// （cmd/mcp-hub-desktop）读同一份声明表、同一套校验，因此"能怎么配"
// 永远只有一个答案。
//
// 优先级：进程环境变量 > 配置文件 > 声明表里的内置默认值。
package config

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/viper"
	"github.com/subosito/gotenv"

	"github.com/hydra/mcp-hub/internal/hub/aggregate"
	"github.com/hydra/mcp-hub/internal/search/client"
	"github.com/hydra/mcp-hub/internal/search/corpus"
)

// EnvPrefix 是所有配置变量的前缀。
const EnvPrefix = "MCPHUB_"

// 配置项的环境变量名。声明表、取值代码、帮助清单都只引用这些常量，
// 字符串只在这里出现一次——加一个配置项只动常量与声明表两处。
const (
	EnvDB             = EnvPrefix + "DB"
	EnvDBDriver       = EnvPrefix + "DB_DRIVER"
	EnvCache          = EnvPrefix + "CACHE"
	EnvCacheDriver    = EnvPrefix + "CACHE_DRIVER"
	EnvAddr           = EnvPrefix + "ADDR"
	EnvAPIKey         = EnvPrefix + "API_KEY"
	EnvLogLevel       = EnvPrefix + "LOG_LEVEL"
	EnvListMode       = EnvPrefix + "LIST_MODE"
	EnvSSE            = EnvPrefix + "SSE"
	EnvProbeInterval  = EnvPrefix + "PROBE_INTERVAL"
	EnvProbeOnStart   = EnvPrefix + "PROBE_ON_START"
	EnvProbeBlock     = EnvPrefix + "PROBE_BLOCK_AFTER"
	EnvEmbedBaseURL   = EnvPrefix + "EMBED_BASE_URL"
	EnvEmbedAPIKey    = EnvPrefix + "EMBED_API_KEY"
	EnvEmbedModel     = EnvPrefix + "EMBED_MODEL"
	EnvEmbedDims      = EnvPrefix + "EMBED_DIMS"
	EnvEmbedBatchSize = EnvPrefix + "EMBED_BATCH_SIZE"
	EnvCorpusFields   = EnvPrefix + "CORPUS_FIELDS"
	EnvRerankModel    = EnvPrefix + "RERANK_MODEL"
	EnvRerankBaseURL  = EnvPrefix + "RERANK_BASE_URL"
	EnvRerankAPIKey   = EnvPrefix + "RERANK_API_KEY"
	EnvFile           = EnvPrefix + "ENV_FILE"
	EnvGinMode        = EnvPrefix + "GIN_MODE"
)

// 存储/缓存后端的取值。声明表的 choices、取值函数、文档都引用它们。
const (
	DriverAuto     = "auto"
	DriverPostgres = "postgres"
	DriverSQLite   = "sqlite"

	DriverRedis  = "redis"
	DriverMemory = "memory"
)

// DefaultProbeInterval 是周期探活的默认间隔，声明表与命令行共用同一个值。
const DefaultProbeInterval = 45 * time.Second

// ginModeEnv 是 gin 自己认的环境变量名（gin.EnvGinMode）。
// 这里不 import gin 就为了一个字符串常量——配置层不该依赖 HTTP 框架；
// 它以"同义名"的形式挂在 MCPHUB_GIN_MODE 上（见 configItem.aliases）。
const ginModeEnv = "GIN_MODE"

// Settings 是引擎运行需要的全部配置。
//
// 这是部署形态无关的：命令行与桌面壳拿到的都是这一个结构体。
type Settings struct {
	// DB 是数据库位置：postgres:// 连接串，或 sqlite 的文件路径。
	DB string
	// DBDriver 是数据库后端：auto | postgres | sqlite。
	// auto 按 DB 的形态推断（postgres:// 前缀即 postgres，否则 sqlite）。
	DBDriver string

	// Cache 是缓存位置：redis:// 连接串。memory 后端忽略本项。
	Cache string
	// CacheDriver 是缓存后端：auto | redis | memory。
	// auto 按 Cache 的形态推断（redis:// 前缀即 redis，空串即 memory）。
	CacheDriver string

	Addr string

	ProbeInterval   time.Duration
	ProbeOnStart    bool
	ProbeBlockAfter int
	EnableSSE       bool
	LogLevel        string
	ListMode        string
	GinMode         string

	APIKey string

	EmbedBaseURL string
	EmbedAPIKey  string
	EmbedModel   string
	EmbedDims    int
	EmbedBatch   int
	CorpusFields corpus.Fields

	RerankBaseURL string
	RerankAPIKey  string
	RerankModel   string

	// 能跑但明显不对的配置，启动时提醒一下，不拦着。
	Warnings []string
}

// ResolvedDBDriver 返回推断之后的具体后端名（绝不会是 auto）。
func (s Settings) ResolvedDBDriver() string {
	if s.DBDriver != DriverAuto && s.DBDriver != "" {
		return s.DBDriver
	}
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(s.DB)), "postgres://") ||
		strings.HasPrefix(strings.ToLower(strings.TrimSpace(s.DB)), "postgresql://") {
		return DriverPostgres
	}
	return DriverSQLite
}

// ResolvedCacheDriver 返回推断之后的具体后端名（绝不会是 auto）。
func (s Settings) ResolvedCacheDriver() string {
	if s.CacheDriver != DriverAuto && s.CacheDriver != "" {
		return s.CacheDriver
	}
	lower := strings.ToLower(strings.TrimSpace(s.Cache))
	if strings.HasPrefix(lower, "redis://") || strings.HasPrefix(lower, "rediss://") {
		return DriverRedis
	}
	return DriverMemory
}

// cfgKind 决定一项配置怎么解析、怎么校验。
type cfgKind int

const (
	kindString       cfgKind = iota // 原样字符串
	kindInt                         // 整数
	kindBool                        // 布尔
	kindDuration                    // Go 时长（45s / 2m）
	kindEnum                        // 取值必须在 choices 里
	kindCorpusFields                // 逗号分隔的语料字段集合
	kindRedisURL                    // redis:// 或 rediss:// 连接串
)

// configItem 是一项配置的完整声明：名字、类型、默认值、必填与否、可选值、说明。
// 这张表同时驱动三件事——绑定 viper、启动校验、mcp-hub env 的清单，
// 所以"哪些必填、哪些选填、能填什么"只有一个出处。
type configItem struct {
	env      string   // 环境变量名，也是配置文件里写的名字
	kind     cfgKind  // 取值与校验方式
	def      string   // 内置默认值；空串表示默认就是空
	required bool     // 必填：缺了直接报错（退出码 2）
	choices  []string // 枚举项的可填值

	// aliases 是同义的环境变量名，优先级低于 env：给"外部已有一套约定名"
	// 的开关用（目前只有 GIN_MODE）。让老用法照旧、新名字也能进配置清单。
	aliases []string

	// emptyOverridesDefault 让"显式写空"能压过内置默认值。
	// 只有 RERANK_MODEL 用得到：写空是关掉精排，与"没写"（用默认模型）是两件事。
	emptyOverridesDefault bool

	// stripEndpoint 去掉误粘进来的 /embeddings、/rerank 后缀：客户端自己拼端点，
	// 不去掉就会请求到 /embeddings/embeddings 上。
	stripEndpoint bool

	// bootstrap 项在加载配置文件之前就要用到，只能读进程环境变量，不交给 viper。
	bootstrap bool

	note string // 说明，也是报错文案的一部分
}

// configItems 是全部配置项，顺序即 mcp-hub env 的打印顺序。
var configItems = []configItem{
	{
		env: EnvDB, kind: kindString,
		note: "数据库位置：postgres:// 连接串（如 postgres://user:pw@127.0.0.1:5432/mcphub?sslmode=disable），或 SQLite 文件路径（如 ~/Library/Application Support/mcp-hub/mcp-hub.db）",
	},
	{
		env: EnvDBDriver, kind: kindEnum, def: DriverAuto,
		choices: []string{DriverAuto, DriverPostgres, DriverSQLite},
		note:    "数据库后端；auto 按 MCPHUB_DB 的形态推断（postgres:// 前缀即 postgres，否则 sqlite）",
	},
	{
		env: EnvCache, kind: kindRedisURL,
		note: "Redis 连接串，如 redis://127.0.0.1:6379/0；支持 rediss://（TLS）。选内存后端时留空即可",
	},
	{
		env: EnvCacheDriver, kind: kindEnum, def: DriverAuto,
		choices: []string{DriverAuto, DriverRedis, DriverMemory},
		note:    "缓存后端；auto 按 MCPHUB_CACHE 的形态推断（redis:// 前缀即 redis，留空即 memory）。内存后端不跨进程共享，单机单进程部署才选它",
	},
	{
		env: EnvAddr, kind: kindString, def: ":9090",
		note: "监听地址；端口写 0 由系统分配。只写 :9090 等于监听全部网卡",
	},
	{
		env: EnvAPIKey, kind: kindString,
		note: "管理接口鉴权密钥；留空不校验，仅建议监听 127.0.0.1 时留空",
	},
	{
		env: EnvLogLevel, kind: kindEnum, def: "info",
		choices: []string{"debug", "info", "warn", "error"},
		note:    "日志级别",
	},
	{
		env: EnvListMode, kind: kindEnum, def: aggregate.ListModeMeta,
		choices: []string{aggregate.ListModeMeta, aggregate.ListModeAll},
		note:    "tools/list 列出哪些工具",
	},
	{
		env: EnvProbeInterval, kind: kindDuration, def: DefaultProbeInterval.String(),
		note: "周期探活间隔，0 关闭",
	},
	{
		env: EnvProbeOnStart, kind: kindBool, def: "true",
		note: "启动时先探活一轮再对外服务",
	},
	{
		env: EnvProbeBlock, kind: kindInt, def: "3",
		note: "同一上游连续探活失败达到该次数后暂停它的工具调用（探活照常，恢复自动解除），0 不拦截",
	},
	{
		env: EnvSSE, kind: kindBool, def: "true",
		note: "是否暴露旧版 SSE 端点",
	},
	{
		env: EnvGinMode, kind: kindEnum, def: "release",
		choices: []string{"debug", "release", "test"},
		aliases: []string{ginModeEnv},
		note:    "HTTP 框架（gin）运行模式；debug 会把中间件提示打到 stdout",
	},

	{
		env: EnvEmbedBaseURL, kind: kindString, def: client.DefaultBaseURL, stripEndpoint: true,
		note: "OpenAI 兼容的 embed 服务地址",
	},
	{
		env: EnvEmbedAPIKey, kind: kindString,
		note: "embed 密钥；留空则检索降级为 bm25，网关照常运行",
	},
	{
		env: EnvEmbedModel, kind: kindString, def: client.DefaultModel,
		note: "embed 模型名",
	},
	{
		env: EnvEmbedDims, kind: kindInt, def: strconv.Itoa(client.DefaultDims),
		note: "向量维度，必须与模型一致；填错会在写库前报错",
	},
	{
		env: EnvEmbedBatchSize, kind: kindInt, def: strconv.Itoa(client.DefaultBatch),
		note: "每批送多少条文本给 embed 接口",
	},
	{
		env: EnvCorpusFields, kind: kindCorpusFields, def: "all",
		note: "进语料的字段：" + strings.Join(corpus.FieldNames, ",") + "，或 all",
	},

	{
		env: EnvRerankModel, kind: kindString, def: client.DefaultRerankModel, emptyOverridesDefault: true,
		note: "rerank 模型名；显式写空即关闭精排（hybrid 返回 RRF 融合排序）",
	},
	{
		env: EnvRerankBaseURL, kind: kindString, stripEndpoint: true,
		note: "rerank 服务地址；留空复用 embed",
	},
	{
		env: EnvRerankAPIKey, kind: kindString,
		note: "rerank 密钥；留空复用 embed",
	},

	{
		env: EnvFile, kind: kindString, bootstrap: true,
		note: "显式指定配置文件路径；默认依次找 ./.env 与可执行文件旁的 .env",
	},
}

func (it configItem) requiredLabel() string {
	if it.required {
		return "是"
	}
	return "否"
}

func (it configItem) defaultLabel() string {
	switch {
	case it.def != "":
		return it.def
	case it.required:
		return "（必填）"
	default:
		return "（空）"
	}
}

func (it configItem) usage() string {
	s := it.note
	if len(it.choices) > 0 {
		s += "；可填 " + strings.Join(it.choices, " | ")
	}
	if len(it.aliases) > 0 {
		s += "；同义变量 " + strings.Join(it.aliases, "、") + "（本项优先）"
	}
	return s
}

// configItemByEnv 按环境变量名查声明项："这名字认不认识"只查这一处。
var configItemByEnv = func() map[string]*configItem {
	m := make(map[string]*configItem, len(configItems))
	for i := range configItems {
		m[configItems[i].env] = &configItems[i]
	}
	return m
}()

// keyOf 把环境变量名折成 viper 键名。viper 读配置文件时会把键统一折成小写，
// 键名与"小写的环境变量名"对齐，配置文件里的 MCPHUB_DB 与进程环境里的
// MCPHUB_DB 才会落到同一个键上，进程环境变量也就自然压过配置文件。
func keyOf(env string) string { return strings.ToLower(env) }

// cfg 是全局唯一的配置实例，优先级：进程环境变量 > 配置文件 > 表里的内置默认值。
// 默认值不交给 viper.SetDefault，只在声明表里写一份。
var cfg = newConfigViper()

// newConfigViper 按声明表建一个实例：每个配置项绑定它自己的环境变量名
// （以及声明的同义名，本名优先——viper 按绑定顺序取第一个存在的）。
func newConfigViper() *viper.Viper {
	v := viper.New()
	// 允许"显式写空"作为有效取值：RERANK_MODEL 写空是关掉精排，
	// 与"没写"（用内置默认模型）是两件不同的事，取值时要能分辨。
	v.AllowEmptyEnv(true)
	for i := range configItems {
		it := &configItems[i]
		if it.bootstrap {
			// 配置文件路径本身不能由配置文件决定。
			continue
		}
		// BindEnv 是纯可变参数函数（key 在第一位），Go 不允许"普通实参 + 展开切片"
		// 混用，所以要把 key、env、aliases 合成一个切片再一次展开。
		// 环境变量名的先后即优先级：env 在 aliases 前面，本项名优先。
		v.BindEnv(append([]string{keyOf(it.env), it.env}, it.aliases...)...)
	}
	return v
}

// reader 是所有配置的取值口：先按环境变量名取字符串，再按表里声明的类型解析。
// 不直接用 viper.GetInt/GetBool 是为了给出明确的中文错误，
// 并把一类配置错误攒起来一次报完，而不是改一个跑一次。
type reader struct {
	errs  []string
	warns []string
}

func (r *reader) item(env string) *configItem { return configItemByEnv[env] }

// raw 是"用户实际写了什么"：没写或写了等于没写（空、只有空格）都是空串。
func (r *reader) raw(env string) string {
	return strings.TrimSpace(cfg.GetString(keyOf(env)))
}

// str 空值一律回落到内置默认值。声明了 emptyOverridesDefault 的项例外：
// 显式写空就是空串（关掉功能），与"没写"（用默认值）不是一回事，
// 所以那种项要看 IsSet 而不是看值是否为空。
func (r *reader) str(env string) string {
	it := r.item(env)
	if it.emptyOverridesDefault && cfg.IsSet(keyOf(env)) {
		return r.raw(env)
	}
	if v := r.raw(env); v != "" {
		return v
	}
	return it.def
}

// 下面三个类型化取值都是"总函数"：解析不了就回落默认值。
// validate 已经先跑过一轮，真有问题时根本走不到这里，
// 所以它们不必再回报错，返回值也就能直接用。
func (r *reader) integer(env string) int {
	if v := r.raw(env); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	def, _ := strconv.Atoi(r.item(env).def)
	return def
}

func (r *reader) boolean(env string) bool {
	if v := r.raw(env); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	def, _ := strconv.ParseBool(r.item(env).def)
	return def
}

func (r *reader) duration(env string) time.Duration {
	if v := r.raw(env); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	def, _ := time.ParseDuration(r.item(env).def)
	return def
}

func (r *reader) baseURL(env string) string {
	v := strings.TrimRight(r.str(env), "/")
	if v == "" {
		return ""
	}
	if !r.item(env).stripEndpoint {
		return v
	}
	for _, suffix := range []string{"/embeddings", "/rerank"} {
		if strings.HasSuffix(v, suffix) {
			fixed := strings.TrimSuffix(v, suffix)
			r.warns = append(r.warns, fmt.Sprintf(
				"%s 带上了端点后缀，已按 %q 使用（客户端会自己拼 %s）", env, fixed, suffix))
			return fixed
		}
	}
	return v
}

func (r *reader) corpusFields(env string) corpus.Fields {
	f, err := corpus.ParseFields(r.raw(env))
	if err != nil {
		// validate 已经报过这一项，这里只是兜底：拿不准就按默认口径。
		return corpus.DefaultFields()
	}
	return f
}

// IsRedisURL 判连接串是不是本项目支持的那两种形态。
func IsRedisURL(spec string) bool {
	lower := strings.ToLower(strings.TrimSpace(spec))
	return strings.HasPrefix(lower, "redis://") || strings.HasPrefix(lower, "rediss://")
}

// IsPostgresURL 判连接串是不是 PostgreSQL DSN。
func IsPostgresURL(dsn string) bool {
	lower := strings.ToLower(strings.TrimSpace(dsn))
	return strings.HasPrefix(lower, "postgres://") || strings.HasPrefix(lower, "postgresql://")
}

// validate 逐项检查"必填 + 类型 + 取值"，一次把问题报完。
func (r *reader) validate() {
	for i := range configItems {
		it := &configItems[i]
		raw := r.raw(it.env)

		if it.required && raw == "" {
			r.errs = append(r.errs, fmt.Sprintf("缺少必填项 %s：%s", it.env, it.note))
		}
		if raw == "" {
			continue
		}

		switch it.kind {
		case kindInt:
			if _, err := strconv.Atoi(raw); err != nil {
				r.errs = append(r.errs, fmt.Sprintf("%s=%q 不是整数", it.env, raw))
			}
		case kindBool:
			if _, err := strconv.ParseBool(raw); err != nil {
				r.errs = append(r.errs, fmt.Sprintf("%s=%q 不是布尔值（用 true / false / 1 / 0）", it.env, raw))
			}
		case kindDuration:
			if _, err := time.ParseDuration(raw); err != nil {
				r.errs = append(r.errs, fmt.Sprintf("%s=%q 不是合法时长（用 45s / 2m）", it.env, raw))
			}
		case kindEnum:
			if !slices.Contains(it.choices, raw) {
				r.errs = append(r.errs, fmt.Sprintf("%s 只能是 %s，收到 %q",
					it.env, strings.Join(it.choices, " | "), raw))
			}
		case kindCorpusFields:
			if _, err := corpus.ParseFields(raw); err != nil {
				r.errs = append(r.errs, fmt.Sprintf("%s: %v", it.env, err))
			}
		case kindRedisURL:
			// 连接串形态在这里判掉：业务侧拿到的一定是能用的东西。
			// 显式选了 redis 才严判；auto 形态认不出来的由 validateBackends 报，
			// 选了内存后端时这一项本就该留空。
			if r.raw(EnvCacheDriver) == DriverRedis && !IsRedisURL(raw) {
				r.errs = append(r.errs, fmt.Sprintf("%s 必须是 redis:// 或 rediss:// 连接串，收到 %q", it.env, raw))
			}
		}
	}

	r.validateBackends()
}

// resolvedCacheDriver 是校验阶段用的后端推断：此时 Settings 还没构造出来。
// 规则与 Settings.ResolvedCacheDriver 一致。枚举值本身合不合法由声明表那一轮
// 负责，这里只在"认得出"时给答案，认不出就回 auto 交给 validateBackends 报错。
func (r *reader) resolvedCacheDriver() string {
	switch d := r.raw(EnvCacheDriver); d {
	case "", DriverAuto:
		// auto：留空即内存后端；形态像 redis 即 redis；
		// 其余（含拼错的 redis 连接串）不猜，交给校验报错。
		switch spec := r.raw(EnvCache); {
		case spec == "":
			return DriverMemory
		case IsRedisURL(spec):
			return DriverRedis
		default:
			return DriverAuto
		}
	case DriverRedis, DriverMemory:
		return d
	default:
		return DriverAuto
	}
}

// resolvedDBDriver 是校验阶段用的后端推断。
func (r *reader) resolvedDBDriver() string {
	switch d := r.raw(EnvDBDriver); d {
	case "", DriverAuto:
		// auto：postgres:// 前缀即 postgres；留空即 sqlite（默认文件路径由
		// 调用方给，见 engine.Options.DataDir）；其余不猜。
		switch db := r.raw(EnvDB); {
		case db == "":
			return DriverSQLite
		case IsPostgresURL(db):
			return DriverPostgres
		case strings.Contains(db, "://"):
			return DriverAuto
		default:
			return DriverSQLite
		}
	case DriverPostgres, DriverSQLite:
		return d
	default:
		return DriverAuto
	}
}

// enumOK 判断某个枚举项的实际取值是否合法。声明表那一轮已经报过错，
// 这里只是让 validateBackends 别在同一个错误上再叠一条无关的。
func (r *reader) enumOK(env string) bool {
	it := r.item(env)
	raw := r.raw(env)
	return raw == "" || slices.Contains(it.choices, raw)
}

// validateBackends 校验"后端 × 连接串"这一对组合，这是两个配置项彼此约束的地方，
// 单看任何一项都判不出来。
func (r *reader) validateBackends() {
	db := r.raw(EnvDB)
	cache := r.raw(EnvCache)
	explicitDB := r.raw(EnvDBDriver)
	explicitCache := r.raw(EnvCacheDriver)

	switch {
	case !r.enumOK(EnvDBDriver):
		// 枚举本身就不合法，validate 已经报过，这里不再叠加。
	case r.resolvedDBDriver() == DriverPostgres:
		if db == "" {
			r.errs = append(r.errs, fmt.Sprintf("%s=%s 时 %s 必填", EnvDBDriver, DriverPostgres, EnvDB))
		} else if !IsPostgresURL(db) {
			r.errs = append(r.errs, fmt.Sprintf("%s=%s 时 %s 必须是 postgres:// 连接串，收到 %q",
				EnvDBDriver, DriverPostgres, EnvDB, db))
		}
	case r.resolvedDBDriver() == DriverSQLite:
		if IsPostgresURL(db) {
			r.errs = append(r.errs, fmt.Sprintf("%s=%s 时 %s 应填文件路径，收到 postgres 连接串 %q",
				EnvDBDriver, DriverSQLite, EnvDB, db))
		}
	default:
		// 没显式选后端，且 MCPHUB_DB 的形态认不出来（不是空、不是 postgres://、
		// 又不是纯文件路径）——这种多半是写错了，不能当文件路径用。
		if explicitDB == "" || explicitDB == DriverAuto {
			r.errs = append(r.errs, fmt.Sprintf(
				"%s 的值 %q 认不出后端：要么是 postgres:// 连接串，要么是 SQLite 文件路径；也可以显式设 %s",
				EnvDB, db, EnvDBDriver))
		}
	}

	switch {
	case !r.enumOK(EnvCacheDriver):
		// 同上，枚举非法的错误已经报过。
	case r.resolvedCacheDriver() == DriverRedis:
		if cache == "" {
			r.errs = append(r.errs, fmt.Sprintf("%s=%s 时 %s 必填", EnvCacheDriver, DriverRedis, EnvCache))
		}
	case r.resolvedCacheDriver() == DriverMemory:
		if cache != "" && explicitCache == DriverMemory {
			r.warns = append(r.warns, fmt.Sprintf(
				"%s=%s 时 %s 不会生效，已按内存缓存运行（取值 %q）",
				EnvCacheDriver, DriverMemory, EnvCache, cache))
		}
	default:
		r.errs = append(r.errs, fmt.Sprintf(
			"%s 的值 %q 认不出后端：要么是 redis:// 连接串，要么留空走内存缓存；也可以显式设 %s",
			EnvCache, cache, EnvCacheDriver))
	}
}

// Load 把所有配置一次读齐、一次报完错。
func Load() (Settings, error) {
	r := &reader{}
	r.validate()

	s := Settings{
		DB:          r.str(EnvDB),
		DBDriver:    r.str(EnvDBDriver),
		Cache:       r.str(EnvCache),
		CacheDriver: r.str(EnvCacheDriver),
		Addr:        r.str(EnvAddr),

		ProbeInterval:   r.duration(EnvProbeInterval),
		ProbeOnStart:    r.boolean(EnvProbeOnStart),
		ProbeBlockAfter: r.integer(EnvProbeBlock),
		EnableSSE:       r.boolean(EnvSSE),
		LogLevel:        r.str(EnvLogLevel),
		ListMode:        r.str(EnvListMode),
		GinMode:         r.str(EnvGinMode),

		APIKey: r.str(EnvAPIKey),

		EmbedBaseURL: r.baseURL(EnvEmbedBaseURL),
		EmbedAPIKey:  r.str(EnvEmbedAPIKey),
		EmbedModel:   r.str(EnvEmbedModel),
		EmbedDims:    r.integer(EnvEmbedDims),
		EmbedBatch:   r.integer(EnvEmbedBatchSize),
		CorpusFields: r.corpusFields(EnvCorpusFields),

		RerankBaseURL: r.baseURL(EnvRerankBaseURL),
		RerankAPIKey:  r.str(EnvRerankAPIKey),
		RerankModel:   r.str(EnvRerankModel),

		Warnings: r.warns,
	}

	if len(r.errs) > 0 {
		return Settings{}, fmt.Errorf("%s", strings.Join(r.errs, "；"))
	}
	return s, nil
}

// String 从当前配置实例里读一项声明过的配置，只取值、不校验、不填默认值。
// 给 add / servers 这类"只要一两项、不跑整套配置"的子命令用：
// 读的是同一个 viper，所以与 serve 看到的值一定一致（进程环境变量 > 配置文件）。
func String(env string) string {
	return strings.TrimSpace(cfg.GetString(keyOf(env)))
}

// PrintEnvHelp 打印配置清单：变量名 / 必填 / 默认值 / 说明。
func PrintEnvHelp(out io.Writer) {
	fmt.Fprintf(out, "\n  mcp-hub 配置项（写在 .env 或进程环境变量里）\n\n")
	fmt.Fprintf(out, "  %-24s %-4s %-26s %s\n", "变量", "必填", "默认值", "说明")
	for _, it := range configItems {
		fmt.Fprintf(out, "  %-24s %-4s %-26s %s\n",
			it.env, it.requiredLabel(), it.defaultLabel(), it.usage())
	}
	fmt.Fprintf(out, "\n  优先级：进程环境变量 > .env > 内置默认。\n"+
		"  配置文件查找顺序：$MCPHUB_ENV_FILE → ./.env → 可执行文件旁的 .env，只加载第一个命中的。\n"+
		"  桌面端（mcp-hub-desktop）查找顺序是：$MCPHUB_ENV_FILE → <应用数据目录>/.env，\n"+
		"  不回退到当前工作目录 —— 它的工作目录完全不可控。\n"+
		"  .env 里不认识的 MCPHUB_* 名字会在启动时点名提醒。\n"+
		"  配置只有这一个来源：改配置 = 改这里 + 重启，管理界面上没有配置项。\n"+
		"  改了 EMBED_MODEL / EMBED_DIMS 后重启会重建向量，期间检索\n"+
		"  退回 bm25，构建完成自动恢复。\n\n")
}

// File 描述一次配置文件加载的结果。
type File struct {
	Path    string   // 实际加载的文件；空表示一个都没找到
	Loaded  int      // 文件里认识的配置项个数
	Unknown []string // 文件里带 MCPHUB_ 前缀但不认识的键
}

// LoadFile 定位并加载配置文件：只用第一个命中的文件。
// 进程环境变量里的值优先于文件（viper 的环境变量层本来就压过配置文件层）。
//
// 文件里的键还会照旧注入进程环境变量（已有的键不覆盖）：serve / probe / add
// 拉起的 stdio 子进程继承父进程环境，配置文件里给下游准备的变量不能断在这里。
func LoadFile() (File, error) { return LoadFileFrom("") }

// LoadFileFrom 与 LoadFile 相同，但把查找锚定在一个目录上（桌面入口
// 用它指向应用数据目录下的配置）。
//
// 锚定意味着**不回退**：explicit 不存在时不会转头去读 ./.env，
// 结果是"没有配置"而不是"读到了别人的配置"。理由见 resolveConfigFile。
func LoadFileFrom(explicit string) (File, error) {
	path := resolveConfigFile(explicit)
	if path == "" {
		return File{}, nil
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return File{}, fmt.Errorf("读取 %s 失败: %w", path, err)
	}
	// 用 viper 内置 dotenv 编解码器所用的同一个库解析，行为与 ReadInConfig 一致，
	// 同时拿到原始键名——既进 viper 的配置层，也补进进程环境变量。
	vars, err := gotenv.StrictParse(bytes.NewReader(raw))
	if err != nil {
		return File{}, fmt.Errorf("解析 %s 失败: %w", path, err)
	}

	merged := make(map[string]any, len(vars))
	for k, v := range vars {
		merged[k] = v
		if _, exists := os.LookupEnv(k); exists {
			continue
		}
		if err := os.Setenv(k, v); err != nil {
			return File{}, fmt.Errorf("设置 %s 失败: %w", k, err)
		}
	}
	// 同时进 viper 的配置层：即使将来不再注入进程环境变量，配置照旧生效。
	if err := cfg.MergeConfigMap(merged); err != nil {
		return File{}, fmt.Errorf("载入 %s 失败: %w", path, err)
	}

	loaded, unknown := scanEnvKeys(raw)
	return File{Path: path, Loaded: loaded, Unknown: unknown}, nil
}

// resolveConfigFile 按顺序找配置文件，返回第一个存在的；都没有则返回空串。
//
// 两种查找口径，由 explicit 是否为空决定：
//
//	explicit == ""（命令行入口）  $MCPHUB_ENV_FILE → ./.env → 可执行文件旁的 .env
//	explicit != ""（桌面入口）    $MCPHUB_ENV_FILE → explicit，到此为止
//
// 第二种刻意不回退到 ./.env：桌面形态的配置位置是它自己的一部分
// （应用数据目录），而它的工作目录完全不可控——双击启动时是 / 或
// System32，从终端启动时是随手进的某个目录。让"同一个二进制会读到
// 哪份配置"取决于你在哪敲的命令，是那种只在别人机器上复现的 bug；
// 在这里它是真的发生过：桌面端站在仓库里启动，读到了仓库的 .env，
// 于是一个理应零配置可用的应用去连了并不存在的 PostgreSQL。
//
// 真要覆盖位置，用 $MCPHUB_ENV_FILE——那是一个明确写下来的意图，
// 而不是"碰巧站在哪儿"。
//
// $MCPHUB_ENV_FILE 只能从进程环境变量读——它决定去哪找配置，
// 不能反过来由配置决定。两个口径都把它排在第一位。
func resolveConfigFile(explicit string) string {
	candidates := make([]string, 0, 3)
	if p := strings.TrimSpace(os.Getenv(EnvFile)); p != "" {
		candidates = append(candidates, p)
	}

	if explicit != "" {
		return firstExisting(append(candidates, explicit))
	}

	candidates = append(candidates, ".env")
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), ".env"))
	}
	return firstExisting(candidates)
}

// firstExisting 返回候选里第一个存在的普通文件；都没有则返回空串。
func firstExisting(candidates []string) string {
	for _, p := range candidates {
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p
		}
	}
	return ""
}

// scanEnvKeys 数一下文件里写了几个认识的配置项，并点名不认识的 MCPHUB_* 项。
// 把 MCPHUB_EMBED_DIMS 写成 MCPHUB_EMBED_DIM 这种错，不点名就只剩"改了没生效"。
func scanEnvKeys(raw []byte) (loaded int, unknown []string) {
	seen := make(map[string]struct{})
	for line := range strings.Lines(string(raw)) {
		name, ok := envLineKey(line)
		if !ok {
			continue
		}
		upper := strings.ToUpper(name)
		if _, dup := seen[upper]; dup {
			continue
		}
		seen[upper] = struct{}{}

		if _, known := configItemByEnv[upper]; known {
			loaded++
			continue
		}
		if strings.HasPrefix(upper, EnvPrefix) {
			unknown = append(unknown, name)
		}
	}
	return loaded, unknown
}

// envLineKey 从配置文件的一行里取出键名，忽略空行、注释与 "export " 前缀。
func envLineKey(line string) (string, bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", false
	}
	line = strings.TrimPrefix(line, "export ")
	name, _, ok := strings.Cut(line, "=")
	if !ok {
		return "", false
	}
	name = strings.TrimSpace(name)
	return name, name != ""
}
