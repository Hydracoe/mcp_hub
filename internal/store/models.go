package store

import (
	"encoding/json"
	"time"

	"github.com/hydra/mcp-hub/internal/downstream"
)

// 这一层是"持久化形态"，与对外的领域结构体刻意分开。
//
// 理由有两条：
//   1. 存进去的样子决定了锁与索引，而领域结构体是被上层到处传的；
//      混在一起会让"给列改个名"变成一次跨包重构。
//   2. 列里的 JSON、布尔、时间戳都用最朴素的存储类型（TEXT / INTEGER / BIGINT），
//      这样 PG 与 SQLite 的建表结果一致，换库不需要改数据。
//
// 这里只声明"列怎么读写"，不声明主键之外的约束与索引——
// 建表由 schema.sql 独占，GORM 不参与 DDL（理由见该文件抬头）。
// 表名、列名、列序都必须与 schema.sql 一一对应。

// serverRow 是 servers 表的一行。
type serverRow struct {
	ID               string `gorm:"column:id;primaryKey"`
	Key              string `gorm:"column:key"`
	Name             string `gorm:"column:name"`
	Transport        string `gorm:"column:transport"`
	URL              string `gorm:"column:url"`
	Headers          string `gorm:"column:headers"` // JSON object
	AuthMode         string `gorm:"column:auth_mode"`
	Command          string `gorm:"column:command"`
	Args             string `gorm:"column:args"` // JSON array
	Env              string `gorm:"column:env"`  // JSON array
	TimeoutMS        int64  `gorm:"column:timeout_ms"`
	ProtocolVersion  string `gorm:"column:protocol_version"`
	LegacyOnly       int64  `gorm:"column:legacy_only"`
	ToolFilterMode   string `gorm:"column:tool_filter_mode"`
	ToolFilterList   string `gorm:"column:tool_filter_list"` // JSON array
	EstTokensPerCall int64  `gorm:"column:est_tokens_per_call"`
	Enabled          int64  `gorm:"column:enabled"`

	// 元数据（PRD §4.7）。instructions 由探活自动落库，
	// 其余三项人工维护。
	Description  string `gorm:"column:description"`
	Tags         string `gorm:"column:tags"`         // JSON array
	Instructions string `gorm:"column:instructions"` // 下游 serverInfo.instructions
	CategoryID   string `gorm:"column:category_id"`  // 单值，无外键

	Status        string `gorm:"column:status"`
	ProtocolEra   string `gorm:"column:protocol_era"`
	ServerName    string `gorm:"column:server_name"`
	ServerVersion string `gorm:"column:server_version"`
	Capabilities  string `gorm:"column:capabilities"` // JSON array
	LastCheckAt   *int64 `gorm:"column:last_check_at"`
	LastLatencyMS *int64 `gorm:"column:last_latency_ms"`
	LastErrorKind string `gorm:"column:last_error_kind"`
	LastError     string `gorm:"column:last_error"`
	CreatedMS     int64  `gorm:"column:created_at"`
	UpdatedMS     int64  `gorm:"column:updated_at"`
}

func (serverRow) TableName() string { return "servers" }

// toolRow 是 tools 表的一行，即某个 server 的工具缓存。
type toolRow struct {
	ID          string `gorm:"column:id;primaryKey"`
	ServerID    string `gorm:"column:server_id"`
	Name        string `gorm:"column:name"`
	ExposedName string `gorm:"column:exposed_name"`
	Description string `gorm:"column:description"`
	InputSchema string `gorm:"column:input_schema"` // JSON Schema
	Enabled     int64  `gorm:"column:enabled"`
	CachedMS    int64  `gorm:"column:cached_at"`
}

func (toolRow) TableName() string { return "tools" }

// logRow 是 call_logs 表的一行。
type logRow struct {
	ID           string `gorm:"column:id;primaryKey"`
	TS           int64  `gorm:"column:ts"`
	ServerID     string `gorm:"column:server_id"`
	ServerKey    string `gorm:"column:server_key"`
	ToolName     string `gorm:"column:tool_name"`
	ExposedName  string `gorm:"column:exposed_name"`
	DurationMS   int64  `gorm:"column:duration_ms"`
	Status       string `gorm:"column:status"`
	ErrorKind    string `gorm:"column:error_kind"`
	ErrorText    string `gorm:"column:error"`
	CredentialID string `gorm:"column:credential_id"`
	APIKeyID     string `gorm:"column:api_key_id"`
	Args         string `gorm:"column:args"`
	Result       string `gorm:"column:result"`
}

func (logRow) TableName() string { return "call_logs" }

// apiKeyRow 是 api_keys 表的一行。
//
// 只存 sha256：库被拖走也拿不到可用凭据。prefix（明文前 8 位）
// 仅为让用户在列表里认出"这是哪把 key"。
type apiKeyRow struct {
	ID         string `gorm:"column:id;primaryKey"`
	Name       string `gorm:"column:name"`
	Prefix     string `gorm:"column:prefix"`
	KeyHash    string `gorm:"column:key_hash"`
	Enabled    int64  `gorm:"column:enabled"`
	LastUsedMS int64  `gorm:"column:last_used_at"`
	CreatedMS  int64  `gorm:"column:created_at"`
	UpdatedMS  int64  `gorm:"column:updated_at"`
}

func (apiKeyRow) TableName() string { return "api_keys" }

// credentialRow 是 credentials 表的一行。
//
// 一行 = 一组完整的注入头，而不是"某个头字段"——供应商的凭据形态
// 各不相同（Bearer / X-API-Key / 多个头组合），只有整组才是一次
// 可独立轮换的最小单位。
type credentialRow struct {
	ID       string `gorm:"column:id;primaryKey"`
	ServerID string `gorm:"column:server_id"`
	Label    string `gorm:"column:label"`
	Headers  string `gorm:"column:headers"` // JSON object
	Enabled  int64  `gorm:"column:enabled"`

	// 持久状态只有 active / disabled 两种。cooling 是内存态，刻意不落库：
	// 重启往往就是为了恢复，把限流状态持久化只会让新进程继续背旧账。
	State        string `gorm:"column:state"`
	DisabledKind string `gorm:"column:disabled_kind"`
	DisabledAt   *int64 `gorm:"column:disabled_at"`
	DisabledNote string `gorm:"column:disabled_note"`

	RPMLimit int64 `gorm:"column:rpm_limit"`
	TPMLimit int64 `gorm:"column:tpm_limit"`

	SortOrder int64 `gorm:"column:sort_order"`
	CreatedMS int64 `gorm:"column:created_at"`
	UpdatedMS int64 `gorm:"column:updated_at"`
}

func (credentialRow) TableName() string { return "credentials" }

// categoryRow 是 categories 表的一行。
type categoryRow struct {
	ID          string `gorm:"column:id;primaryKey"`
	Name        string `gorm:"column:name"`
	Description string `gorm:"column:description"`
	CreatedMS   int64  `gorm:"column:created_at"`
	UpdatedMS   int64  `gorm:"column:updated_at"`
}

func (categoryRow) TableName() string { return "categories" }

func (r categoryRow) toCategory() Category {
	return Category{
		ID:          r.ID,
		Name:        r.Name,
		Description: r.Description,
		CreatedAt:   time.UnixMilli(r.CreatedMS),
		UpdatedAt:   time.UnixMilli(r.UpdatedMS),
	}
}

// embeddingRow 是 tool_embeddings 表的一行。
//
// vector 刻意存 TEXT（JSON 数组）：schema.sql 是 PG 与 SQLite 共用的，
// pgvector 的 vector 类型只有 PG 有。PG 查询时用 `vector::vector` 强转
// 做 ANN；SQLite 方言下向量检索链路整体关闭（降级 keyword/bm25）。
type embeddingRow struct {
	ToolID     string `gorm:"column:tool_id;primaryKey"`
	Model      string `gorm:"column:model"`
	Dims       int64  `gorm:"column:dims"`
	Vector     string `gorm:"column:vector"` // JSON array
	CorpusHash string `gorm:"column:corpus_hash"`
	CreatedMS  int64  `gorm:"column:created_at"`
}

func (embeddingRow) TableName() string { return "tool_embeddings" }

func (r embeddingRow) toEmbedding() Embedding {
	e := Embedding{
		ToolID:     r.ToolID,
		Model:      r.Model,
		Dims:       int(r.Dims),
		CorpusHash: r.CorpusHash,
		CreatedAt:  time.UnixMilli(r.CreatedMS),
	}
	if err := json.Unmarshal([]byte(r.Vector), &e.Vector); err != nil {
		e.Vector = nil
	}
	return e
}

// configRow 是 app_config 表的一行。
type configRow struct {
	Key   string `gorm:"column:key;primaryKey"`
	Value string `gorm:"column:value"` // JSON
}

func (configRow) TableName() string { return "app_config" }

// --- 行 → 领域结构体 ---

func (r serverRow) toServer() Server {
	srv := Server{
		Spec: downstream.Spec{
			ID:              r.ID,
			Key:             r.Key,
			Name:            r.Name,
			Transport:       downstream.Transport(r.Transport),
			URL:             r.URL,
			Headers:         decodeStringMap(r.Headers),
			AuthMode:        r.AuthMode,
			Command:         r.Command,
			Args:            decodeStringSlice(r.Args),
			Env:             decodeStringSlice(r.Env),
			Timeout:         time.Duration(r.TimeoutMS) * time.Millisecond,
			ProtocolVersion: r.ProtocolVersion,
			LegacyOnly:      intToBool(r.LegacyOnly),
		},
		Enabled:          intToBool(r.Enabled),
		Status:           r.Status,
		Description:      r.Description,
		Tags:             decodeStringSlice(r.Tags),
		Instructions:     r.Instructions,
		CategoryID:       r.CategoryID,
		ToolFilterMode:   ToolFilterMode(r.ToolFilterMode).Normalize(),
		ToolFilterList:   decodeStringSlice(r.ToolFilterList),
		EstTokensPerCall: int(r.EstTokensPerCall),
		ProtocolEra:      r.ProtocolEra,
		ServerName:       r.ServerName,
		ServerVersion:    r.ServerVersion,
		Capabilities:     decodeStringSlice(r.Capabilities),
		LastErrorKind:    downstream.ErrorKind(r.LastErrorKind),
		LastError:        r.LastError,
		CreatedAt:        time.UnixMilli(r.CreatedMS),
		UpdatedAt:        time.UnixMilli(r.UpdatedMS),
	}
	if r.LastLatencyMS != nil {
		srv.LastLatencyMS = *r.LastLatencyMS
	}
	if r.LastCheckAt != nil {
		srv.LastCheckAt = time.UnixMilli(*r.LastCheckAt)
	}
	return srv
}

func (r toolRow) toTool() Tool {
	return Tool{
		ID:          r.ID,
		ServerID:    r.ServerID,
		Name:        r.Name,
		ExposedName: r.ExposedName,
		Description: r.Description,
		InputSchema: r.InputSchema,
		Enabled:     intToBool(r.Enabled),
		CachedAt:    time.UnixMilli(r.CachedMS),
	}
}

func (r logRow) toLogEntry() LogEntry {
	return LogEntry{
		ID:           r.ID,
		TS:           time.UnixMilli(r.TS),
		ServerID:     r.ServerID,
		ServerKey:    r.ServerKey,
		ToolName:     r.ToolName,
		ExposedName:  r.ExposedName,
		DurationMS:   r.DurationMS,
		Status:       r.Status,
		ErrorKind:    downstream.ErrorKind(r.ErrorKind),
		Error:        r.ErrorText,
		CredentialID: r.CredentialID,
		APIKeyID:     r.APIKeyID,
		Args:         r.Args,
		Result:       r.Result,
	}
}

func (r apiKeyRow) toAPIKey() APIKey {
	return APIKey{
		ID:         r.ID,
		Name:       r.Name,
		Prefix:     r.Prefix,
		Enabled:    intToBool(r.Enabled),
		LastUsedAt: time.UnixMilli(r.LastUsedMS),
		CreatedAt:  time.UnixMilli(r.CreatedMS),
		UpdatedAt:  time.UnixMilli(r.UpdatedMS),
	}
}

func (r credentialRow) toCredential() Credential {
	c := Credential{
		ID:           r.ID,
		ServerID:     r.ServerID,
		Label:        r.Label,
		Headers:      decodeStringMap(r.Headers),
		Enabled:      intToBool(r.Enabled),
		State:        r.State,
		DisabledKind: downstream.KeyVerdict(r.DisabledKind),
		DisabledNote: r.DisabledNote,
		RPMLimit:     int(r.RPMLimit),
		TPMLimit:     int(r.TPMLimit),
		SortOrder:    int(r.SortOrder),
		CreatedAt:    time.UnixMilli(r.CreatedMS),
		UpdatedAt:    time.UnixMilli(r.UpdatedMS),
	}
	if c.State == "" {
		c.State = CredStateActive
	}
	if r.DisabledAt != nil {
		c.DisabledAt = time.UnixMilli(*r.DisabledAt)
	}
	return c
}
