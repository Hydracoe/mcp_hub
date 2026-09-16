/**
 * 管理 API 的类型定义。
 *
 * 与后端 `internal/api/dto.go` / `credentials.go` / `query.go` 一一对应，
 * 命名保持驼峰。改动这里之前先看后端 —— 这些字段是接口契约，
 * 不是前端可以自己捏的形状。
 */

/** 下游传输类型。 */
export type Transport = 'stdio' | 'sse' | 'streamable_http'

/** server 健康状态。 */
export type ServerStatus = 'healthy' | 'degraded' | 'offline' | 'unknown'

/** 工具过滤模式。`allow_all` 放行全部，`allow`/`block` 按名单过滤。 */
export type ToolFilterMode = 'allow_all' | 'allow' | 'block'

/** 生效的鉴权形态。`none` 是「这个下游天生不需要密钥」，不等于「还没配」。 */
export type AuthMode = 'none' | 'header'

/** 密钥状态。 */
export type CredentialState = 'active' | 'cooling' | 'disabled'

/** 密钥被弃用的原因。 */
export type DisabledKind = 'INVALID_KEY' | 'KEY_REVOKED' | 'QUOTA_EXHAUSTED'

/** 下游失败分类（PRD §4.2.3）。用于日志筛选与诊断文案。 */
export const ERROR_KINDS = [
  'DNS_FAIL',
  'CONN_REFUSED',
  'TLS_FAIL',
  'TIMEOUT',
  'AUTH_REQUIRED',
  'NOT_FOUND',
  'UPSTREAM_ERROR',
  'RATE_LIMITED',
  'QUOTA_EXHAUSTED',
  'NOT_MCP',
  'PROTOCOL_MISMATCH',
  'CAPABILITY_MISSING',
  'SPAWN_FAILED',
  'EXIT_EARLY',
  'HANDSHAKE_TIMEOUT',
  'SERVER_DRAINING',
  'CANCELED',
  'NO_USABLE_KEY',
  'UNKNOWN',
] as const
export type ErrorKind = (typeof ERROR_KINDS)[number] | ''

/** 密钥调度的判决（PRD §7.5.1）。与 ErrorKind 是两回事，不要混用。 */
export type KeyVerdict = 'NEUTRAL' | 'RATE_LIMITED' | 'QUOTA_EXHAUSTED' | 'INVALID_KEY' | 'KEY_REVOKED'

/** 统一错误体：`{"code": "...", "error": "..."}`。 */
export type ApiErrorCode =
  | 'not_found'
  | 'key_taken'
  /** 分类中文名已被占用 */
  | 'category_taken'
  | 'invalid_request'
  | 'unauthorized'
  | 'unavailable'
  | 'internal'
  /** 密钥池不支持（stdio 下游） */
  | 'key_pool_unsupported'
  | 'import_failed'

/* ────────────────────────────── 概览 ────────────────────────────── */

export interface ServerStats {
  total: number
  enabled: number
  healthy: number
  degraded: number
  offline: number
  unknown: number
}

export interface ToolStats {
  /** 聚合快照里的工具数。 */
  total: number
  /** 下游声明的工具总数（缓存）。与 total 不等即说明过滤正在生效。 */
  cached?: number
  /** 被过滤掉、未进入聚合池的数量。 */
  filtered?: number
  /** 暴露出去的工具数。 */
  exposed?: number
}

export interface CallStats {
  total: number
  failed: number
  avgMs: number
  maxMs: number
  /** 后端已算好（0–1）。前端不要自己再算一遍，分母取法容易不一致。 */
  failureRate: number
}

/**
 * 密钥池汇总。
 *
 * 三种「用不上」分开计数是有意的：冷却会自己好、弃用需要换密钥、
 * 人工停用需要人改主意 —— 三种下一步动作完全不同。
 */
export interface KeySummary {
  total: number
  /** 配了密钥池的 server 数。未配的走单密钥模式，不在统计内。 */
  servers: number
  usable: number
  cooling: number
  disabled: number
  /** cooling + disabled，后端直接给，避免前端两处各加一遍。 */
  unusable: number
}

export interface OverviewResponse {
  ready: boolean
  builtAt: number
  uptimeSeconds: number
  /** 版本号。后端 B6 缺口补上之前可能是空串，界面要能兜底。 */
  version?: string
  servers: ServerStats
  tools: ToolStats
  calls: CallStats
  credentials: KeySummary
  endpoints: Record<string, string>
  /** 缺口 B3：每把不可用密钥的运行时明细（冷却剩余 / 弃用原因），overview「密钥池状态明细」段使用。 */
  keyDetails?: KeyDetail[]
}

/** 单把密钥的运行时明细（overview 的密钥池明细段，B3）。 */
export interface KeyDetail {
  serverId: string
  serverKey: string
  credentialId: string
  label: string
  state: CredentialState
  disabledKind?: DisabledKind
  disabledNote?: string
  /** 冷却剩余毫秒。state === 'cooling' 时有意义。 */
  coolingRemainingMs?: number
  lastError?: string
  lastUsedAt?: number
}

/* ────────────────────────────── Server ────────────────────────────── */

export interface ServerSummary {
  id: string
  key: string
  name: string
  transport: Transport
  /** URL 或命令行，列表直接展示。 */
  target: string

  enabled: boolean
  status: ServerStatus
  authMode: AuthMode

  protocolEra?: 'modern' | 'legacy'
  protocolVersion?: string
  serverName?: string
  serverVersion?: string
  capabilities?: string[]

  description?: string
  tags?: string[]
  categoryId?: string
  category?: string

  /** 下游声明的工具总数（缓存）。 */
  toolCount: number
  /** 过滤后真正进入聚合池的数量。与 toolCount 不等即过滤在生效。 */
  exposedToolCount: number
  /** 被停用的工具数（列表页工具子列的「启用 6 / 共 8」）。 */
  enabledToolCount?: number

  toolFilterMode: ToolFilterMode
  toolFilterList?: string[]

  /** false 表示该 server 走单密钥模式（沿用 servers.headers）。 */
  keyPool: boolean
  keyCount: number
  usableKeyCount: number
  coolingKeyCount?: number
  disabledKeyCount?: number
  /** 密钥调用成功率（0–1），列表页密钥子列展示。 */
  keySuccessRate?: number

  /** TPM 预估回退值。0 表示不计量 —— 界面必须显示「未计量」而非 0。 */
  estTokensPerCall?: number

  /** 已从聚合列表撤下，但还有调用在收尾。与 status 是两回事。 */
  draining: boolean
  drainingInflight?: number
  drainingSince?: number

  /** 工具调用成功率（0–1）。 */
  toolSuccessRate?: number

  lastCheckAt?: number
  lastLatencyMs?: number
  lastErrorKind?: ErrorKind
  lastError?: string
  lastErrorHint?: string
  createdAt: number
  updatedAt: number
}

/** 可编辑的连接配置。密钥字段一律以 `***` 回显。 */
export interface ServerConfigView {
  url?: string
  headers?: Record<string, string>
  /** 配了哪些头（键名）。设计稿的脱敏形态：只给键名 + 指纹。 */
  headerKeys?: string[]
  authMode?: AuthMode
  command?: string
  args?: string[]
  env?: string[]
  timeoutMs: number
  protocolVersion?: string
  legacyOnly: boolean
  estTokensPerCall?: number
}

export interface ServerDetail extends ServerSummary {
  config: ServerConfigView
  /** 下游原始工具名。 */
  tools?: string[]
  /**
   * 探活结果随创建/刷新返回，避免前端并发两次请求
   * （PRD FR-2.1：写操作后立刻拿到 L1–L3 状态，不必再打一次 /test）。
   */
  probe?: ProbeResult
  /** 该 server 的密钥池汇总（详情页密钥段使用，由 mock 在 GET 详情时附上）。 */
  credentials?: KeySummary
}

export interface ServerRequest {
  key: string
  name: string
  transport: Transport
  url?: string
  headers?: Record<string, string>
  authMode?: AuthMode
  command?: string
  args?: string[]
  env?: string[]
  timeoutMs?: number
  protocolVersion?: string
  legacyOnly?: boolean
  toolFilterMode?: ToolFilterMode
  toolFilterList?: string[]
  estTokensPerCall?: number
  description?: string
  tags?: string[]
  categoryId?: string
  enabled?: boolean
}

/**
 * 干跑探活（POST /api/servers/probe，配置尚未落库）。
 *
 * 新增向导的「探活 → 选工具 → 暴露」流程靠它：连接结论之外还带
 * 下游工具清单，供用户勾选哪些对外暴露。探活失败时 ok=false，
 * 错误分类与排查提示在 kind / hint / detail 里。
 */
export interface ProbeDraftResult {
  ok: boolean
  /** 下游自报的 server 名与协商到的协议版本。 */
  serverName?: string
  serverVersion?: string
  protocolVersion?: string
  legacy?: boolean
  capabilities?: string[]
  toolCount: number
  /** 下游原始工具名与描述，供勾选暴露。 */
  tools?: { name: string; description?: string }[]
  /** 工具在聚合端点上的对外名（带 key 前缀），与 tools 一一对应。 */
  exposedTools?: string[]
  /** 失败信息。 */
  stage?: string
  kind?: string
  detail?: string
  stderr?: string
  /** 中文排查方向。 */
  hint?: string
}

/* ─────────────────────── 探活 / 试调 ─────────────────────── */

/** 探活的一个阶段（L1 可达性 / L2 协议 / L3 工具清单）。 */
export interface ProbeLevel {
  level: 'L1' | 'L2' | 'L3' | 'L4'
  label: string
  ok: boolean
  durationMs: number
  errorKind?: ErrorKind
  detail?: string
  /** stdio 下游失败时的 stderr 摘要 —— 定位 stdio 问题唯一的线索。 */
  stderr?: string
}

export interface ProbeResult {
  ok: boolean
  /** 协商到的协议版本。 */
  protocolVersion?: string
  protocolEra?: 'modern' | 'legacy'
  serverName?: string
  serverVersion?: string
  capabilities?: string[]
  toolCount?: number
  levels: ProbeLevel[]
  totalMs: number
  errorKind?: ErrorKind
  error?: string
  /** 中文排查方向。 */
  hint?: string
}

/** 全量测试的汇总。healthy + failed 恒等于 total，停用项单列。 */
export interface TestAllResponse {
  total: number
  healthy: number
  failed: number
  /** 停用数量，不计入成败 —— 它是用户主动做的决定，不是故障。 */
  disabled: number
  tookMs: number
  results: ProbeResultItem[]
  testedAt: number
}

export interface ProbeResultItem {
  serverId: string
  serverKey: string
  name: string
  ok: boolean
  enabled: boolean
  status: ServerStatus
  latencyMs?: number
  protocolVersion?: string
  toolCount?: number
  errorKind?: ErrorKind
  error?: string
  hint?: string
}

/* ────────────────────────────── 工具 ────────────────────────────── */

export interface ToolEntry {
  exposedName: string
  name: string
  serverId: string
  serverKey: string
  description?: string
  /** JSON Schema。 */
  inputSchema?: Record<string, unknown>
  /** 下游不可用时为 false —— 工具仍在列表里，调用会返回带原因的失败。 */
  available: boolean

  /** 以下为界面需要、由列表接口聚合出的统计（可选）。 */
  callCount?: number
  successRate?: number
  enabled?: boolean
  category?: string
}

/* ────────────────────────────── 日志 ────────────────────────────── */

export interface LogItem {
  id: string
  /** RFC3339。后端用 time.Time 序列化。 */
  ts: string
  serverId: string
  serverKey: string
  toolName: string
  exposedName: string
  durationMs: number
  status: 'ok' | 'error'
  errorKind?: ErrorKind
  error?: string
  credentialId?: string
  /** 调用方（上游接入密钥）的 id，非明文。匿名调用为空。 */
  apiKeyId?: string
  /**
   * 调用方名字的**快照**，即调用发生时那把密钥叫什么。
   *
   * 与「接入密钥」页的当前名字可能不同：key 后来改了名或被删掉，
   * 历史行照旧显示当时的名字。这是刻意的 —— 日志是历史事实。
   * 老库升级上来的行没有这一列，为空。
   */
  apiKeyName?: string
  /** 调用参数（已截断）。 */
  args?: string
  /** 调用结果（已截断）。 */
  result?: string
}

export interface LogListResponse {
  total: number
  limit: number
  offset: number
  items: LogItem[]
}

/**
 * 日志可排序的列。
 *
 * 与后端 `store.logSortColumns` 的键一一对应 —— 那边做白名单映射，
 * 传了表里没有的值会静默回退到 `ts`，所以这里多一个值不会 400，
 * 但会静默失效。加列时两边一起改。
 */
export const LOG_SORTS = ['ts', 'durationMs', 'status'] as const

export type LogSort = (typeof LOG_SORTS)[number]

export type LogOrder = 'asc' | 'desc'

export const DEFAULT_LOG_SORT: LogSort = 'ts'
export const DEFAULT_LOG_ORDER: LogOrder = 'desc'

/**
 * 按调用方筛选时「匿名」的保留值。
 *
 * 必须与后端 `store.LogCallerAnonymous` 一致：空串在查询参数里表示
 * "不筛"，所以"只看匿名"需要一个非空字面量来表达 —— 否则它会被
 * 后端理解成"看全部"，筛出来的恰好是相反的结果。
 */
export const LOG_CALLER_ANONYMOUS = 'anonymous'

export interface LogQuery {
  serverId?: string
  status?: 'ok' | 'error'
  kind?: ErrorKind
  /**
   * 按调用方（接入密钥）筛。
   *
   * `'anonymous'` 是保留值，筛的是匿名调用 —— 空串在这里表示"不筛"，
   * 所以"只看匿名"必须另有词来表达（对应后端 `store.LogCallerAnonymous`）。
   */
  apiKeyId?: string
  q?: string
  since?: number
  until?: number
  /** 排序列，默认 `ts`。 */
  sort?: LogSort
  /** 排序方向，默认 `desc`（最新在前）。 */
  order?: LogOrder
  limit?: number
  offset?: number
}

/** 密钥事件。后端目前只写 slog（B1 缺口），这里先按前端内存流呈现。 */
export interface KeyEventItem {
  id: string
  ts: string
  serverId: string
  serverKey: string
  type: 'KEY_COOLDOWN' | 'KEY_DISABLED' | 'KEY_RECOVERED'
  credentialId: string
  credentialLabel?: string
  detail?: string
}

/* ────────────────────────────── 密钥 ────────────────────────────── */

export interface CredentialView {
  id: string
  label: string
  /** 配了哪些头（键名）。不回显 `***` 的 headers 映射。 */
  headerKeys?: string[]
  /** 值末 4 位。用来区分「是哪一把」，不是用来验证密钥对不对。 */
  fingerprint?: string
  configured: boolean

  enabled: boolean
  state: CredentialState

  disabledKind?: DisabledKind
  disabledAt?: number
  disabledNote?: string

  coolingUntil?: number
  coolingForMs?: number
  consecutiveLimited?: number

  rpmLimit: number
  tpmLimit: number

  rpmUsed: number
  tpmUsed: number
  /** false 时界面必须显示「未计量」，而不是 "0 / 100k"。 */
  tpmMetered: boolean
  windowEnd?: number

  /** 被轮询选中的次数。 */
  picks: number
  lastUsedAt?: number
  lastError?: string

  /** 界面统计（可选）。 */
  callCount?: number
  failCount?: number
  successRate?: number
  p95Ms?: number

  createdAt: number
  updatedAt: number
}

export interface CredentialListResponse {
  total: number
  items: CredentialView[]
  /** false 表示退回单密钥模式，界面要说清这件事。 */
  keyPool: boolean
}

export interface CredentialRequest {
  label: string
  headers?: Record<string, string>
  /** 下面三个二选一：直接给 headers，或给 value + 展开方式。 */
  value?: string
  authScheme?: 'bearer' | 'x-api-key' | 'header'
  headerName?: string
  rpmLimit?: number
  tpmLimit?: number
}

export interface CredentialImportRequest {
  authScheme?: 'bearer' | 'x-api-key' | 'header'
  headerName?: string
  /** 一行一把，"名称=值" 或直接给值。 */
  lines: string[]
  rpmLimit?: number
  tpmLimit?: number
}

/* ─────────────────────── 分类 / 元数据 ─────────────────────── */

export interface Category {
  id: string
  /** 中文名：界面展示。 */
  name: string
  /**
   * 英文名：进对外工具名的第一段（`<slug>__<serverKey>__<toolName>`）。
   *
   * 与中文名分开是必须的——工具名只能是 [a-z0-9-]，
   * 中文名折叠后只剩空串，前缀段会退化成随机 id。
   */
  slug: string
  description: string
  serverCount?: number
  toolCount?: number
  createdAt: number
  updatedAt: number
}

/* ─────────────────────── 接入 API Key ─────────────────────── */

/**
 * 一把接入密钥：上游 AI 客户端调用 `/mcp` 时出示的凭据，由网关自己签发。
 *
 * 两套"密钥"必须分清，否则界面上的名字会互相误导：
 * - 「密钥池」＝ 网关拿去调**下游** server 的凭据，按 Server 隔离；
 * - 「接入密钥」＝ **上游** 客户端调网关时出示的凭据，全局一份清单。
 *
 * 字段与后端 apikeyItem 一一对应。刻意没有调用次数：v1 只记
 * lastUsedAt，界面不要凭空造一个后端不存在的统计量。
 */
export interface ApiKeyView {
  id: string
  /** 标识调用方，例如「Claude Desktop · 我的 Mac」。创建时必填。 */
  name: string
  /** 明文前 10 位（含 `mh-` 前缀），列表里认人用。明文本身不回显。 */
  prefix: string
  enabled: boolean
  /** Unix 毫秒；0 表示从未被使用过。 */
  lastUsedAt: number
  createdAt: number
}

/**
 * 创建响应：明文 `key` 只在这一次出现。
 *
 * 库里只存 sha256，之后任何接口都只回 prefix —— 丢了只能删掉重建。
 * 所以界面必须在创建成功后立刻交付它，并说清"关掉就看不到了"。
 */
export interface ApiKeyCreateResponse extends ApiKeyView {
  key: string
}

/* ────────────────────────────── 接入 ────────────────────────────── */

export interface ConnectResponse {
  mcpEndpoint: string
  sseEndpoint?: string
  healthEndpoint: string
  /** 一键复制的客户端配置：{"mcp-hub": "<url>"}。 */
  mcpServers: Record<string, string>
  toolCount: number
  serverCount: number
  summary: string
  note: string
}

/* ─────────────────────── 嵌入检索配置（M7） ─────────────────────── */

export interface EmbedConfig {
  baseUrl: string
  model: string
  dims: number
  rerankModel?: string
  rerankEnabled: boolean
  /** 语料字段勾选。默认「工具描述 + instructions」。 */
  corpusFields: string[]
  fusionWeights?: { bm25: number; vector: number }
  retrieveTopN: number
  configured: boolean
}

export interface EmbedRebuildStatus {
  running: boolean
  total: number
  done: number
  failed: number
  startedAt?: number
  finishedAt?: number
}
