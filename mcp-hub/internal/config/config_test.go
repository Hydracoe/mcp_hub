package config

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hydra/mcp-hub/internal/hub/aggregate"
	"github.com/hydra/mcp-hub/internal/search/client"
	"github.com/hydra/mcp-hub/internal/search/corpus"
)

// isolateConfig 让每个用例从"什么都没配"开始：把所有已知配置项从进程环境里摘掉，
// 再换一个干净的配置实例（viper 是全局的，上一个用例的值会留在里面）。
// 用例自己按需 t.Setenv。
func isolateConfig(t *testing.T) {
	t.Helper()
	keepEnv(t)
	for _, it := range configItems {
		os.Unsetenv(it.env)
		for _, alias := range it.aliases {
			os.Unsetenv(alias)
		}
	}
	cfg = newConfigViper()
}

// keepEnv 记下测试开始时的 MCPHUB_* 变量，结束时把新增的清掉、原有的还原。
// LoadFile 会把配置文件里的键注入进程环境，不还原就会漏给下一个用例。
func keepEnv(t *testing.T) {
	t.Helper()
	before := envSnapshot()
	t.Cleanup(func() {
		now := envSnapshot()
		for k := range now {
			if _, existed := before[k]; !existed {
				os.Unsetenv(k)
			}
		}
		for k, v := range before {
			os.Setenv(k, v)
		}
	})
}

func envSnapshot() map[string]string {
	m := map[string]string{}
	for _, kv := range os.Environ() {
		k, v, ok := strings.Cut(kv, "=")
		if ok && strings.HasPrefix(k, EnvPrefix) {
			m[k] = v
		}
	}
	return m
}

// 声明表是"哪些必填、能填什么"的唯一出处，它自己得先站得住：
// 默认值必须能被本项类型解析、枚举默认值必须在可选值里、每一项都得有说明。
func TestConfigItemsDeclared(t *testing.T) {
	seen := make(map[string]bool, len(configItems))
	for _, it := range configItems {
		if !strings.HasPrefix(it.env, EnvPrefix) {
			t.Errorf("配置项 %q 少了 %s 前缀", it.env, EnvPrefix)
		}
		if seen[it.env] {
			t.Errorf("配置项 %s 重复声明", it.env)
		}
		seen[it.env] = true
		// 同义名不能是另一个配置项的本名：viper 按绑定顺序取第一个存在的值，
		// 撞名会让其中一项永远读不到。
		for _, alias := range it.aliases {
			if configItemByEnv[alias] != nil {
				t.Errorf("%s 的同义名 %s 与另一个配置项重名", it.env, alias)
			}
		}

		if it.note == "" {
			t.Errorf("%s 缺说明：mcp-hub env 的清单与启动报错文案都靠它", it.env)
		}
		if it.kind == kindEnum && len(it.choices) < 2 {
			t.Errorf("%s 是枚举项却没有可选值", it.env)
		}
		if it.def == "" {
			continue
		}

		switch it.kind {
		case kindInt:
			if _, err := strconv.Atoi(it.def); err != nil {
				t.Errorf("%s 的默认值 %q 不是整数: %v", it.env, it.def, err)
			}
		case kindBool:
			if _, err := strconv.ParseBool(it.def); err != nil {
				t.Errorf("%s 的默认值 %q 不是布尔值: %v", it.env, it.def, err)
			}
		case kindDuration:
			if _, err := time.ParseDuration(it.def); err != nil {
				t.Errorf("%s 的默认值 %q 不是时长: %v", it.env, it.def, err)
			}
		case kindCorpusFields:
			if _, err := corpus.ParseFields(it.def); err != nil {
				t.Errorf("%s 的默认值 %q 不是合法语料口径: %v", it.env, it.def, err)
			}
		case kindEnum:
			if !slices.Contains(it.choices, it.def) {
				t.Errorf("%s 的默认值 %q 不在可选值 %v 里", it.env, it.def, it.choices)
			}
		}
	}
}

func TestLoadDefaults(t *testing.T) {
	isolateConfig(t)
	t.Setenv(EnvDB, "postgres://127.0.0.1:5432/mcphub")
	t.Setenv(EnvCache, "redis://127.0.0.1:6379/0")

	got, err := Load()
	if err != nil {
		t.Fatalf("只填连接串不该报错: %v", err)
	}
	want := Settings{
		DB:              "postgres://127.0.0.1:5432/mcphub",
		DBDriver:        DriverAuto,
		Cache:           "redis://127.0.0.1:6379/0",
		CacheDriver:     DriverAuto,
		Addr:            "127.0.0.1:9090",
		ProbeInterval:   DefaultProbeInterval,
		ProbeOnStart:    true,
		ProbeBlockAfter: 3,
		EnableSSE:       true,
		LogLevel:        "info",
		ListMode:        aggregate.ListModeMeta,
		GinMode:         "release",
		EmbedBaseURL:    client.DefaultBaseURL,
		EmbedModel:      client.DefaultModel,
		EmbedDims:       client.DefaultDims,
		EmbedBatch:      client.DefaultBatch,
		CorpusFields:    corpus.DefaultFields(),
		RerankModel:     client.DefaultRerankModel,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("默认配置不对:\n got %+v\nwant %+v", got, want)
	}
}

// 两个后端都不配时应能跑起来：数据库落到 sqlite（路径由调用方给），
// 缓存落到进程内实现。这是桌面端"零配置可用"的前提。
func TestLoadZeroConfig(t *testing.T) {
	isolateConfig(t)

	got, err := Load()
	if err != nil {
		t.Fatalf("零配置不该报错: %v", err)
	}
	if got.ResolvedDBDriver() != DriverSQLite {
		t.Errorf("零配置下数据库后端 = %q，期望 sqlite", got.ResolvedDBDriver())
	}
	if got.ResolvedCacheDriver() != DriverMemory {
		t.Errorf("零配置下缓存后端 = %q，期望 memory", got.ResolvedCacheDriver())
	}
}

// 后端推断：连接串形态决定 auto 的走向。
func TestBackendInference(t *testing.T) {
	cases := []struct {
		name        string
		db          string
		dbDriver    string
		cache       string
		cacheDriver string
		wantDB      string
		wantCache   string
		wantErr     string
	}{
		{name: "pg+redis 走形态", db: "postgres://h/db", cache: "redis://h:6379/0", wantDB: DriverPostgres, wantCache: DriverRedis},
		{name: "sqlite 路径", db: "/tmp/x.db", wantDB: DriverSQLite, wantCache: DriverMemory},
		{name: "显式 sqlite 压过形态", db: "/tmp/y.db", dbDriver: DriverSQLite, cacheDriver: DriverMemory, wantDB: DriverSQLite, wantCache: DriverMemory},
		{
			name: "显式 postgres 但填了路径", db: "/tmp/y.db", dbDriver: DriverPostgres,
			cacheDriver: DriverMemory, wantErr: "必须是 postgres:// 连接串",
		},
		{name: "显式 sqlite 但填了 DSN", db: "postgres://h/db", dbDriver: DriverSQLite, wantErr: "应填文件路径"},
		{name: "显式 redis 但没给连接串", db: "/tmp/x.db", cacheDriver: DriverRedis, wantErr: "必填"},
		{name: "缓存串形态认不出", db: "/tmp/x.db", cache: "127.0.0.1:6379", wantErr: "认不出后端"},
		{name: "数据库串形态认不出", db: "mysql://h/db", wantErr: "认不出后端"},
		{name: "枚举非法", db: "/tmp/x.db", dbDriver: "mysql", wantErr: "只能是 auto | postgres | sqlite"},
		{name: "内存后端忽略连接串", db: "/tmp/x.db", cache: "redis://h:6379/0", cacheDriver: DriverMemory, wantDB: DriverSQLite, wantCache: DriverMemory, wantErr: ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			isolateConfig(t)
			setIf(t, EnvDB, c.db)
			setIf(t, EnvDBDriver, c.dbDriver)
			setIf(t, EnvCache, c.cache)
			setIf(t, EnvCacheDriver, c.cacheDriver)

			got, err := Load()
			if c.wantErr != "" {
				if err == nil {
					t.Fatalf("期望报错 %q，实际通过", c.wantErr)
				}
				if !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("报错里没有 %q:\n%v", c.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("不该报错: %v", err)
			}
			if dr := got.ResolvedDBDriver(); dr != c.wantDB {
				t.Errorf("数据库后端 = %q，期望 %q", dr, c.wantDB)
			}
			if cr := got.ResolvedCacheDriver(); cr != c.wantCache {
				t.Errorf("缓存后端 = %q，期望 %q", cr, c.wantCache)
			}
		})
	}
}

func setIf(t *testing.T, env, v string) {
	t.Helper()
	if v != "" {
		t.Setenv(env, v)
	}
}

func TestLoadReportsAllErrors(t *testing.T) {
	isolateConfig(t)
	t.Setenv(EnvDB, "postgres://127.0.0.1:5432/mcphub")
	t.Setenv(EnvCache, "redis://127.0.0.1:6379/0")
	t.Setenv(EnvEmbedDims, "abc")
	t.Setenv(EnvSSE, "yes")
	t.Setenv(EnvProbeInterval, "45")
	t.Setenv(EnvListMode, "everything")
	t.Setenv(EnvCorpusFields, "toolName,typo")

	_, err := Load()
	if err == nil {
		t.Fatal("非法取值应报错")
	}
	for _, want := range []string{
		`MCPHUB_EMBED_DIMS="abc" 不是整数`,
		`MCPHUB_SSE="yes" 不是布尔值`,
		`MCPHUB_PROBE_INTERVAL="45" 不是合法时长`,
		`MCPHUB_LIST_MODE 只能是 meta | all`,
		`未知的语料字段 "typo"`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("错误信息里没有 %q:\n%v", want, err)
		}
	}
}

// RERANK_MODEL 的"显式写空"是关掉精排，和"没写"（用内置默认模型）不是一回事。
func TestRerankModelExplicitEmpty(t *testing.T) {
	isolateConfig(t)
	t.Setenv(EnvDB, "postgres://127.0.0.1:5432/mcphub")
	t.Setenv(EnvCache, "redis://127.0.0.1:6379/0")

	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.RerankModel != client.DefaultRerankModel {
		t.Fatalf("没写 RERANK_MODEL 应用默认模型，得到 %q", got.RerankModel)
	}

	t.Setenv(EnvRerankModel, "")
	got, err = Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.RerankModel != "" {
		t.Fatalf("显式写空应关掉精排，得到 %q", got.RerankModel)
	}
}

// 有人会把服务文档示例里的 /embeddings 一起粘进来，去掉并提醒，
// 不去掉就会请求到 /embeddings/embeddings 上。
func TestEmbedBaseURLStripsEndpoint(t *testing.T) {
	isolateConfig(t)
	t.Setenv(EnvDB, "postgres://127.0.0.1:5432/mcphub")
	t.Setenv(EnvCache, "redis://127.0.0.1:6379/0")
	t.Setenv(EnvEmbedBaseURL, "https://api.example.com/v1/embeddings/")

	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.EmbedBaseURL != "https://api.example.com/v1" {
		t.Fatalf("端点后缀没去掉: %q", got.EmbedBaseURL)
	}
	if len(got.Warnings) != 1 || !strings.Contains(got.Warnings[0], "/embeddings") {
		t.Fatalf("应提醒一次端点后缀，实际 %v", got.Warnings)
	}
}

func TestLoadFile(t *testing.T) {
	isolateConfig(t)

	path := filepath.Join(t.TempDir(), "mcphub.env")
	content := strings.Join([]string{
		"# 注释不算配置",
		"export MCPHUB_ADDR=:7777",
		"MCPHUB_EMBED_DIMS = 768",
		"MCPHUB_EMBED_DIM=1024",
		"",
		"MCPHUB_NOPE=x",
	}, "\n")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("写测试配置失败: %v", err)
	}
	t.Setenv(EnvFile, path)
	// 同一个键，进程环境变量里也有：环境变量优先。
	t.Setenv(EnvAddr, ":8080")

	cf, err := LoadFileFrom("")
	if err != nil {
		t.Fatalf("加载配置文件失败: %v", err)
	}
	if cf.Path != path {
		t.Errorf("加载的文件 = %q，期望 %q", cf.Path, path)
	}
	if cf.Loaded != 2 {
		t.Errorf("认识的配置项 = %d，期望 2（ADDR 与 EMBED_DIMS）", cf.Loaded)
	}
	// 写错名字要能发现，否则只剩"改了 .env 却没生效"。
	if want := []string{"MCPHUB_EMBED_DIM", "MCPHUB_NOPE"}; !slices.Equal(cf.Unknown, want) {
		t.Errorf("不认识的配置项 = %v，期望 %v", cf.Unknown, want)
	}

	if got := cfg.GetString(keyOf(EnvAddr)); got != ":8080" {
		t.Errorf("进程环境变量应压过文件，得到 %q", got)
	}
	if got := cfg.GetString(keyOf(EnvEmbedDims)); got != "768" {
		t.Errorf("文件里的值应生效，得到 %q", got)
	}
}

// 显式指定的路径优先于 $MCPHUB_ENV_FILE：桌面壳用它指向应用数据目录。
// 锚定查找命中的是 explicit 指向的那个文件。
func TestLoadFileFromAnchorsToExplicit(t *testing.T) {
	isolateConfig(t)

	dir := t.TempDir()
	explicit := filepath.Join(dir, "explicit.env")
	if err := os.WriteFile(explicit, []byte("MCPHUB_ADDR=:1111\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cf, err := LoadFileFrom(explicit)
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	if cf.Path != explicit {
		t.Fatalf("加载的文件 = %q，期望 %q", cf.Path, explicit)
	}
	if got := cfg.GetString(keyOf(EnvAddr)); got != ":1111" {
		t.Fatalf("MCPHUB_ADDR = %q，期望 :1111", got)
	}
}

// 锚定查找**不回退**：explicit 不存在时不能转头去读工作目录的 .env。
// 工作目录不可控，回退会让桌面应用读到仓库的 .env 而启动失败。
func TestLoadFileFromDoesNotFallBack(t *testing.T) {
	isolateConfig(t)

	dir := t.TempDir()
	// 工作目录里摆一份 .env——就在眼前，也不该被读到。
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("MCPHUB_ADDR=:3333\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	cf, err := LoadFileFrom(filepath.Join(dir, "没有这个目录", ".env"))
	if err != nil {
		t.Fatalf("找不到配置文件不该报错: %v", err)
	}
	if cf.Path != "" {
		t.Fatalf("锚定查找不该回退到 ./.env，却读到了 %q", cf.Path)
	}
	if got := cfg.GetString(keyOf(EnvAddr)); got == ":3333" {
		t.Fatalf("工作目录的 .env 被读了进来（MCPHUB_ADDR=%q）", got)
	}
}

// $MCPHUB_ENV_FILE 是用户明确写下来的意图，压过锚定的默认位置。
// 它是这两个口径里唯一"能换掉位置"的开关，所以必须在最前面。
func TestEnvFileWinsOverExplicitAnchor(t *testing.T) {
	isolateConfig(t)

	dir := t.TempDir()
	anchored := filepath.Join(dir, "anchored.env")
	override := filepath.Join(dir, "override.env")
	if err := os.WriteFile(anchored, []byte("MCPHUB_ADDR=:1111\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(override, []byte("MCPHUB_ADDR=:2222\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvFile, override)

	cf, err := LoadFileFrom(anchored)
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	if cf.Path != override {
		t.Fatalf("加载的文件 = %q，期望 $MCPHUB_ENV_FILE 指向的 %q", cf.Path, override)
	}
	if got := cfg.GetString(keyOf(EnvAddr)); got != ":2222" {
		t.Fatalf("MCPHUB_ADDR = %q，期望 :2222", got)
	}
}

// 没找到配置文件不是错误：全靠进程环境变量跑是正常用法。
func TestLoadFileMissing(t *testing.T) {
	isolateConfig(t)
	t.Setenv(EnvFile, filepath.Join(t.TempDir(), "不存在.env"))

	cf, err := LoadFileFrom("")
	if err != nil {
		t.Fatalf("找不到配置文件不该报错: %v", err)
	}
	if cf.Path != "" || cf.Loaded != 0 || len(cf.Unknown) != 0 {
		t.Fatalf("没有配置文件时不应有加载结果: %+v", cf)
	}
}

func TestEnvLineKey(t *testing.T) {
	cases := []struct {
		line string
		want string
		ok   bool
	}{
		{"MCPHUB_DB=x", "MCPHUB_DB", true},
		{"  MCPHUB_DB = x  ", "MCPHUB_DB", true},
		{"export MCPHUB_DB=x", "MCPHUB_DB", true},
		{"# MCPHUB_DB=x", "", false},
		{"", "", false},
		{"NOEQUALS", "", false},
	}
	for _, c := range cases {
		got, ok := envLineKey(c.line)
		if got != c.want || ok != c.ok {
			t.Errorf("envLineKey(%q) = %q, %v；期望 %q, %v", c.line, got, ok, c.want, c.ok)
		}
	}
}

// add / servers 的 --db 缺省值走 String，与 serve 读的是同一份配置。
func TestString(t *testing.T) {
	isolateConfig(t)
	if got := String(EnvDB); got != "" {
		t.Fatalf("没配 MCPHUB_DB 时应为空，得到 %q", got)
	}
	t.Setenv(EnvDB, "postgres://127.0.0.1:5432/mcphub")
	if got := String(EnvDB); got != "postgres://127.0.0.1:5432/mcphub" {
		t.Fatalf("String(DB) = %q", got)
	}
	// 取值不做校验也不填默认值：配错的连接串要原样交出去，
	// 让"连不上"在连接处报错，而不是在这里被悄悄换成默认值。
	t.Setenv(EnvDB, "  mysql://oops  ")
	if got := String(EnvDB); got != "mysql://oops" {
		t.Fatalf("String 应原样返回（去掉首尾空格），得到 %q", got)
	}
}

// GIN_MODE 是 gin 自己的约定名，以同义变量的形式挂在 MCPHUB_GIN_MODE 上：
// 老用法照旧，新名字也能进配置清单。
func TestGinModeAlias(t *testing.T) {
	isolateConfig(t)
	t.Setenv(EnvDB, "postgres://127.0.0.1:5432/mcphub")
	t.Setenv(EnvCache, "redis://127.0.0.1:6379/0")

	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.GinMode != "release" {
		t.Fatalf("没写 GIN_MODE 应默认 release，得到 %q", got.GinMode)
	}

	t.Setenv(ginModeEnv, "debug")
	if got, err = Load(); err != nil {
		t.Fatalf("Load: %v", err)
	} else if got.GinMode != "debug" {
		t.Fatalf("同义变量 GIN_MODE 没生效，得到 %q", got.GinMode)
	}

	// 本名优先于同义名。
	t.Setenv(EnvGinMode, "test")
	if got, err = Load(); err != nil {
		t.Fatalf("Load: %v", err)
	} else if got.GinMode != "test" {
		t.Fatalf("MCPHUB_GIN_MODE 应压过 GIN_MODE，得到 %q", got.GinMode)
	}
}

// 连接串形态在配置层判掉，业务侧（engine 的 openCache）拿到的就是能用的值。
func TestCacheMustBeRedisURLWhenRedis(t *testing.T) {
	isolateConfig(t)
	t.Setenv(EnvDB, "/tmp/mcphub.db")
	t.Setenv(EnvCacheDriver, DriverRedis)

	for _, spec := range []string{"127.0.0.1:6379", "postgres://127.0.0.1:5432/x", "rediss:/x"} {
		t.Setenv(EnvCache, spec)
		_, err := Load()
		if err == nil {
			t.Fatalf("MCPHUB_CACHE=%q 应报错", spec)
		}
		if !strings.Contains(err.Error(), "必须是 redis:// 或 rediss:// 连接串") {
			t.Errorf("MCPHUB_CACHE=%q 的报错没说清形态:\n%v", spec, err)
		}
	}

	for _, spec := range []string{"redis://127.0.0.1:6379/0", "REDISS://host:6379/1"} {
		t.Setenv(EnvCache, spec)
		if _, err := Load(); err != nil {
			t.Fatalf("MCPHUB_CACHE=%q 应当合法: %v", spec, err)
		}
	}
}

// 显式选了内存后端却还留着连接串：不报错，但要提醒它没生效。
func TestMemoryCacheIgnoresSpec(t *testing.T) {
	isolateConfig(t)
	t.Setenv(EnvDB, "/tmp/mcphub.db")
	t.Setenv(EnvCacheDriver, DriverMemory)
	t.Setenv(EnvCache, "redis://127.0.0.1:6379/0")

	got, err := Load()
	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	if len(got.Warnings) == 0 || !strings.Contains(got.Warnings[0], "不会生效") {
		t.Fatalf("应提醒连接串未生效，实际 %v", got.Warnings)
	}
}

// 清单由声明表生成，就不该漏项：漏了说明表与打印两处又对不上了。
func TestPrintEnvHelpListsEveryItem(t *testing.T) {
	var buf bytes.Buffer
	PrintEnvHelp(&buf)
	out := buf.String()
	for _, it := range configItems {
		if !strings.Contains(out, it.env) {
			t.Errorf("清单里少了 %s", it.env)
		}
	}
}
