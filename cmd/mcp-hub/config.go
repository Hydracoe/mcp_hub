package main

import (
	"bytes"
	"fmt"
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

// envPrefix 是所有配置变量的前缀。
const envPrefix = "MCPHUB_"

// 配置项的环境变量名。声明表、取值代码、帮助清单都只引用这些常量，
// 字符串只在这里出现一次——加一个配置项只动常量与声明表两处。
const (
	envDB             = envPrefix + "DB"
	envCache          = envPrefix + "CACHE"
	envAddr           = envPrefix + "ADDR"
	envAPIKey         = envPrefix + "API_KEY"
	envLogLevel       = envPrefix + "LOG_LEVEL"
	envListMode       = envPrefix + "LIST_MODE"
	envSSE            = envPrefix + "SSE"
	envProbeInterval  = envPrefix + "PROBE_INTERVAL"
	envProbeOnStart   = envPrefix + "PROBE_ON_START"
	envProbeBlock     = envPrefix + "PROBE_BLOCK_AFTER"
	envEmbedBaseURL   = envPrefix + "EMBED_BASE_URL"
	envEmbedAPIKey    = envPrefix + "EMBED_API_KEY"
	envEmbedModel     = envPrefix + "EMBED_MODEL"
	envEmbedDims      = envPrefix + "EMBED_DIMS"
	envEmbedBatchSize = envPrefix + "EMBED_BATCH_SIZE"
	envCorpusFields   = envPrefix + "CORPUS_FIELDS"
	envRerankModel    = envPrefix + "RERANK_MODEL"
	envRerankBaseURL  = envPrefix + "RERANK_BASE_URL"
	envRerankAPIKey   = envPrefix + "RERANK_API_KEY"
	envFile           = envPrefix + "ENV_FILE"
	envGinMode        = envPrefix + "GIN_MODE"
)

// ginModeEnv 是 gin 自己认的环境变量名（gin.EnvGinMode）。
// 这里不 import gin 就为了一个字符串常量——配置层不该依赖 HTTP 框架；
// 它以"同义名"的形式挂在 MCPHUB_GIN_MODE 上（见 configItem.aliases）。
const ginModeEnv = "GIN_MODE"

// Settings 是 serve 运行需要的全部配置。
type Settings struct {
	DB    string
	Cache string
	Addr  string

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
	env      string   // 环境变量名，也是 .env 里写的名字
	kind     cfgKind  // 取值与校验方式
	def      string   // 内置默认值；空串表示默认就是空
	required bool     // 必填：缺了 serve 直接退出（退出码 2）
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
		env: envDB, kind: kindString, required: true,
		note: "PostgreSQL 连接串，如 postgres://user:pw@127.0.0.1:5432/mcphub?sslmode=disable",
	},
	{
		env: envCache, kind: kindRedisURL, required: true,
		note: "Redis 连接串，如 redis://127.0.0.1:6379/0；支持 rediss://（TLS）",
	},
	{
		env: envAddr, kind: kindString, def: ":9090",
		note: "监听地址；端口写 0 由系统分配。只写 :9090 等于监听全部网卡",
	},
	{
		env: envAPIKey, kind: kindString,
		note: "管理接口鉴权密钥；留空不校验，仅建议监听 127.0.0.1 时留空",
	},
	{
		env: envLogLevel, kind: kindEnum, def: "info",
		choices: []string{"debug", "info", "warn", "error"},
		note:    "日志级别",
	},
	{
		env: envListMode, kind: kindEnum, def: aggregate.ListModeMeta,
		choices: []string{aggregate.ListModeMeta, aggregate.ListModeAll},
		note:    "tools/list 列出哪些工具",
	},
	{
		env: envProbeInterval, kind: kindDuration, def: defaultProbeInterval.String(),
		note: "周期探活间隔，0 关闭",
	},
	{
		env: envProbeOnStart, kind: kindBool, def: "true",
		note: "启动时先探活一轮再对外服务",
	},
	{
		env: envProbeBlock, kind: kindInt, def: "3",
		note: "同一上游连续探活失败达到该次数后暂停它的工具调用（探活照常，恢复自动解除），0 不拦截",
	},
	{
		env: envSSE, kind: kindBool, def: "true",
		note: "是否暴露旧版 SSE 端点",
	},
	{
		env: envGinMode, kind: kindEnum, def: "release",
		choices: []string{"debug", "release", "test"},
		aliases: []string{ginModeEnv},
		note:    "HTTP 框架（gin）运行模式；debug 会把中间件提示打到 stdout",
	},

	{
		env: envEmbedBaseURL, kind: kindString, def: client.DefaultBaseURL, stripEndpoint: true,
		note: "OpenAI 兼容的 embed 服务地址",
	},
	{
		env: envEmbedAPIKey, kind: kindString,
		note: "embed 密钥；留空则检索降级为 keyword/bm25，网关照常运行",
	},
	{
		env: envEmbedModel, kind: kindString, def: client.DefaultModel,
		note: "embed 模型名",
	},
	{
		env: envEmbedDims, kind: kindInt, def: strconv.Itoa(client.DefaultDims),
		note: "向量维度，必须与模型一致；填错会在写库前报错",
	},
	{
		env: envEmbedBatchSize, kind: kindInt, def: strconv.Itoa(client.DefaultBatch),
		note: "每批送多少条文本给 embed 接口",
	},
	{
		env: envCorpusFields, kind: kindCorpusFields, def: "all",
		note: "进语料的字段：" + strings.Join(corpus.FieldNames, ",") + "，或 all",
	},

	{
		env: envRerankModel, kind: kindString, def: client.DefaultRerankModel, emptyOverridesDefault: true,
		note: "rerank 模型名；显式写空即关闭精排（hybrid 返回 RRF 融合排序）",
	},
	{
		env: envRerankBaseURL, kind: kindString, stripEndpoint: true,
		note: "rerank 服务地址；留空复用 embed",
	},
	{
		env: envRerankAPIKey, kind: kindString,
		note: "rerank 密钥；留空复用 embed",
	},

	{
		env: envFile, kind: kindString, bootstrap: true,
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
// 键名与"小写的环境变量名"对齐，.env 里的 MCPHUB_DB 与进程环境里的
// MCPHUB_DB 才会落到同一个键上，进程环境变量也就自然压过配置文件。
func keyOf(env string) string { return strings.ToLower(env) }

// cfg 是全局唯一的配置实例，优先级：进程环境变量 > .env > 表里的内置默认值。
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

// isRedisURL 判连接串是不是本项目支持的那两种形态。
func isRedisURL(spec string) bool {
	lower := strings.ToLower(spec)
	return strings.HasPrefix(lower, "redis://") || strings.HasPrefix(lower, "rediss://")
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
			// 连接串形态在这里判掉：业务侧（serve 的 openCache）拿到的一定是能用的东西。
			if !isRedisURL(raw) {
				r.errs = append(r.errs, fmt.Sprintf("%s 必须是 redis:// 或 rediss:// 连接串，收到 %q", it.env, raw))
			}
		}
	}
}

// LoadSettings 把所有配置一次读齐、一次报完错。
func LoadSettings() (Settings, error) {
	r := &reader{}
	r.validate()

	s := Settings{
		DB:    r.str(envDB),
		Cache: r.str(envCache),
		Addr:  r.str(envAddr),

		ProbeInterval:   r.duration(envProbeInterval),
		ProbeOnStart:    r.boolean(envProbeOnStart),
		ProbeBlockAfter: r.integer(envProbeBlock),
		EnableSSE:       r.boolean(envSSE),
		LogLevel:        r.str(envLogLevel),
		ListMode:        r.str(envListMode),
		GinMode:         r.str(envGinMode),

		APIKey: r.str(envAPIKey),

		EmbedBaseURL: r.baseURL(envEmbedBaseURL),
		EmbedAPIKey:  r.str(envEmbedAPIKey),
		EmbedModel:   r.str(envEmbedModel),
		EmbedDims:    r.integer(envEmbedDims),
		EmbedBatch:   r.integer(envEmbedBatchSize),
		CorpusFields: r.corpusFields(envCorpusFields),

		RerankBaseURL: r.baseURL(envRerankBaseURL),
		RerankAPIKey:  r.str(envRerankAPIKey),
		RerankModel:   r.str(envRerankModel),

		Warnings: r.warns,
	}

	if len(r.errs) > 0 {
		return Settings{}, fmt.Errorf("%s", strings.Join(r.errs, "；"))
	}
	return s, nil
}

// StringConfig 从当前配置实例里读一项声明过的配置，只取值、不校验、不填默认值。
// 给 add / servers 这类"只要一两项、不跑整套 serve 配置"的子命令用：
// 读的是同一个 viper，所以与 serve 看到的值一定一致（进程环境变量 > .env）。
func StringConfig(env string) string {
	return strings.TrimSpace(cfg.GetString(keyOf(env)))
}

// PrintEnvHelp 打印配置清单：变量名 / 必填 / 默认值 / 说明。
func PrintEnvHelp(out *os.File) {
	fmt.Fprintf(out, "\n  mcp-hub 配置项（写在 .env 或进程环境变量里）\n\n")
	fmt.Fprintf(out, "  %-24s %-4s %-26s %s\n", "变量", "必填", "默认值", "说明")
	for _, it := range configItems {
		fmt.Fprintf(out, "  %-24s %-4s %-26s %s\n",
			it.env, it.requiredLabel(), it.defaultLabel(), it.usage())
	}
	fmt.Fprintf(out, "\n  优先级：进程环境变量 > .env > 内置默认。\n"+
		"  配置文件查找顺序：$MCPHUB_ENV_FILE → ./.env → 可执行文件旁的 .env，只加载第一个命中的。\n"+
		"  .env 里不认识的 MCPHUB_* 名字会在启动时点名提醒。\n"+
		"  配置只有这一个来源：改配置 = 改这里 + 重启，管理界面上没有配置项。\n"+
		"  改了 EMBED_MODEL / EMBED_DIMS 后重启会重建向量，期间检索\n"+
		"  退回 keyword/bm25，构建完成自动恢复。\n\n")
}

// configFile 描述一次配置文件加载的结果。
type configFile struct {
	Path    string   // 实际加载的文件；空表示一个都没找到
	Loaded  int      // 文件里认识的配置项个数
	Unknown []string // 文件里带 MCPHUB_ 前缀但不认识的键
}

// loadConfigFile 定位并加载配置文件：只用第一个命中的文件。
// 进程环境变量里的值优先于文件（viper 的环境变量层本来就压过配置文件层）。
//
// 文件里的键还会照旧注入进程环境变量（已有的键不覆盖）：serve / probe / add
// 拉起的 stdio 子进程继承父进程环境，.env 里给下游准备的变量不能断在这里。
func loadConfigFile() (configFile, error) {
	path := resolveConfigFile()
	if path == "" {
		return configFile{}, nil
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return configFile{}, fmt.Errorf("读取 %s 失败: %w", path, err)
	}
	// 用 viper 内置 dotenv 编解码器所用的同一个库解析，行为与 ReadInConfig 一致，
	// 同时拿到原始键名——既进 viper 的配置层，也补进进程环境变量。
	vars, err := gotenv.StrictParse(bytes.NewReader(raw))
	if err != nil {
		return configFile{}, fmt.Errorf("解析 %s 失败: %w", path, err)
	}

	merged := make(map[string]any, len(vars))
	for k, v := range vars {
		merged[k] = v
		if _, exists := os.LookupEnv(k); exists {
			continue
		}
		if err := os.Setenv(k, v); err != nil {
			return configFile{}, fmt.Errorf("设置 %s 失败: %w", k, err)
		}
	}
	// 同时进 viper 的配置层：即使将来不再注入进程环境变量，配置照旧生效。
	if err := cfg.MergeConfigMap(merged); err != nil {
		return configFile{}, fmt.Errorf("载入 %s 失败: %w", path, err)
	}

	loaded, unknown := scanEnvKeys(raw)
	return configFile{Path: path, Loaded: loaded, Unknown: unknown}, nil
}

// resolveConfigFile 按顺序找配置文件，返回第一个存在的；都没有则返回空串。
// $MCPHUB_ENV_FILE 只能从进程环境变量读——它决定去哪找配置，不能反过来由配置决定。
func resolveConfigFile() string {
	candidates := make([]string, 0, 3)
	if p := strings.TrimSpace(os.Getenv(envFile)); p != "" {
		candidates = append(candidates, p)
	}
	candidates = append(candidates, ".env")
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), ".env"))
	}

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
		if strings.HasPrefix(upper, envPrefix) {
			unknown = append(unknown, name)
		}
	}
	return loaded, unknown
}

// envLineKey 从 .env 的一行里取出键名，忽略空行、注释与 "export " 前缀。
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
