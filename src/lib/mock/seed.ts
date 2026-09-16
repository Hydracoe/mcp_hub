import type {
  Category,
  CredentialView,
  KeyEventItem,
  LogItem,
  ServerDetail,
  ServerRequest,
  ServerSummary,
  ToolEntry,
  Transport,
} from '@/lib/api/types'

/**
 * Mock 种子数据。
 *
 * 全部由固定种子的伪随机数生成 —— 每次刷新页面看到的都是同一批
 * server / 工具 / 日志。随机会让「刚才那个报错的 server 去哪了」
 * 变成不可复现的问题，调试体验极差。
 */

/** mulberry32：32 位确定性 PRNG。 */
function mulberry32(seed: number) {
  let a = seed
  return () => {
    a |= 0
    a = (a + 0x6d2b79f5) | 0
    let t = Math.imul(a ^ (a >>> 15), 1 | a)
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296
  }
}

const rnd = mulberry32(20260915)
const pick = <T,>(arr: readonly T[]): T => arr[Math.floor(rnd() * arr.length)]
const int = (min: number, max: number) => min + Math.floor(rnd() * (max - min + 1))
const chance = (p: number) => rnd() < p

/** 记录生成时刻，所有相对时间都以它为基准。 */
export const SEED_NOW = Date.now()

const MINUTE = 60_000
const HOUR = 60 * MINUTE
const DAY = 24 * HOUR

/* ────────────────────────────── 工具名 ────────────────────────────── */

const GENERIC_TOOLS = [
  'search', 'fetch', 'list', 'get', 'create', 'update', 'delete', 'query',
  'parse', 'extract', 'summarize', 'translate', 'upload', 'download', 'sync',
  'batch', 'inspect', 'validate', 'export', 'preview',
]

const DOMAIN_TOOLS: Record<string, string[]> = {
  'web-search': ['search', 'fetch', 'parse', 'query', 'rank', 'suggest', 'crawl', 'index'],
  'academic-search': ['search_papers', 'get_citation', 'list_journals', 'download_pdf', 'bibtex'],
  'google-translate': ['translate_text', 'detect_language', 'list_languages', 'batch_translate'],
  deepl: ['translate', 'rephrase', 'glossary_list', 'document_translate'],
  'file-system': ['read_file', 'write_file', 'list_dir', 'move', 'delete', 'stat', 'glob'],
  'github-tools': ['create_issue', 'list_prs', 'get_repo', 'create_pr', 'list_commits', 'review_pr'],
  'database-query': ['execute_sql', 'list_tables', 'describe_table', 'explain_query', 'export_csv'],
  browser: ['open', 'click', 'screenshot', 'evaluate', 'wait_for', 'get_text'],
  slack: ['post_message', 'list_channels', 'get_history', 'upload_file', 'search_messages'],
  notion: ['create_page', 'query_database', 'update_block', 'search', 'append_children'],
  jira: ['create_issue', 'transition_issue', 'search_issues', 'add_comment', 'list_boards'],
  confluence: ['create_page', 'update_page', 'search_pages', 'list_spaces'],
  figma: ['get_file', 'list_comments', 'export_node', 'get_components'],
  gmail: ['send_message', 'list_threads', 'get_message', 'create_draft', 'search_mail'],
  calendar: ['list_events', 'create_event', 'update_event', 'find_free_slot'],
  'image-gen': ['generate_image', 'edit_image', 'upscale', 'list_models', 'remove_bg'],
  monitor: ['query_metrics', 'list_alerts', 'get_dashboard', 'create_silence'],
  'k8s': ['list_pods', 'get_logs', 'apply_manifest', 'describe_resource', 'scale_deployment'],
  'ci-cd': ['trigger_pipeline', 'get_build_status', 'list_runs', 'cancel_build'],
}

/** 分类池：中文名与英文名成对。英文名进工具名前缀段，见 exposedNameOf。 */
const CATEGORY_POOL: Array<[name: string, slug: string]> = [
  ['搜索检索', 'search'], ['翻译语言', 'translation'], ['文件存储', 'storage'],
  ['开发工具', 'devtools'], ['数据查询', 'data'], ['办公协作', 'office'],
  ['通讯消息', 'messaging'], ['内容生成', 'content'], ['运维监控', 'ops'],
  ['媒体处理', 'media'],
]

const TAG_POOL = [
  'official', 'community', 'read-only', 'write', 'expensive', 'realtime',
  'internal', 'deprecated', 'beta', 'prod',
]

const SERVER_KEYS = [
  'web-search', 'academic-search', 'google-translate', 'deepl', 'file-system',
  'github-tools', 'database-query', 'translate', 'image-gen', 'browser',
  'slack', 'notion', 'jira', 'confluence', 'figma', 'youtube', 'gmail',
  'calendar', 'maps', 'weather', 'news', 'stock', 'crm', 'erp', 'wiki',
  'ci-cd', 'k8s', 'monitor', 'alert', 'log-query', 'pdf', 'ocr', 'speech',
  'vision', 'code-run', 'sql-lite', 'redis', 'kafka', 's3', 'oss', 'dns',
  'whois', 'qr', 'sms', 'pay', 'old-api', 'transcribe', 'analytics',
]

const ERROR_KIND_POOL = [
  'TIMEOUT', 'CONN_REFUSED', 'RATE_LIMITED', 'UPSTREAM_ERROR',
  'NO_USABLE_KEY', 'NOT_MCP', 'AUTH_REQUIRED', 'SERVER_DRAINING', 'CANCELED',
] as const

/* ────────────────────────────── 生成 ────────────────────────────── */

interface MockTool {
  name: string
  description: string
  inputSchema: Record<string, unknown>
  enabled: boolean
  callCount: number
  failCount: number
}

export interface MockServer {
  id: string
  key: string
  name: string
  transport: Transport
  url?: string
  command?: string
  args?: string[]
  env?: string[]
  headers?: Record<string, string>
  authMode: 'none' | 'header'
  timeoutMs: number
  protocolVersion?: string
  legacyOnly: boolean

  enabled: boolean
  status: ServerSummary['status']
  draining: boolean
  drainingInflight?: number
  drainingSince?: number

  description?: string
  tags: string[]
  categoryId?: string
  categoryName?: string
  categorySlug?: string

  toolFilterMode: ServerSummary['toolFilterMode']
  toolFilterList: string[]

  estTokensPerCall: number

  protocolEra?: 'modern' | 'legacy'
  serverName?: string
  serverVersion?: string
  capabilities: string[]

  lastCheckAt?: number
  lastLatencyMs?: number
  lastErrorKind?: string
  lastError?: string

  createdAt: number
  updatedAt: number

  tools: MockTool[]
  creds: CredentialView[]
}

function makeSchema(toolName: string): Record<string, unknown> {
  const needsQuery = /search|query|translate|find|get|list|read|fetch|generate/.test(toolName)
  const props: Record<string, unknown> = {}
  const required: string[] = []
  if (needsQuery) {
    props.query = { type: 'string', description: '查询内容' }
    required.push('query')
  } else {
    props.id = { type: 'string', description: '目标对象 ID' }
    required.push('id')
  }
  props.limit = { type: 'integer', description: '返回条数上限', default: 20, minimum: 1, maximum: 200 }
  props.dryRun = { type: 'boolean', description: '仅预演，不产生副作用', default: false }
  return {
    type: 'object',
    properties: props,
    required,
    additionalProperties: false,
    $comment: `mock schema for ${toolName}`,
  }
}

function credLabel(i: number) {
  return i === 0 ? '主号' : `备用-${i}`
}

function makeCredentials(key: string, count: number, disabledRatio: number): CredentialView[] {
  const out: CredentialView[] = []
  const prefix = key.split('-')[0]
  const state: CredentialView['state'] = 'active'
  for (let i = 0; i < count; i++) {
    let st: CredentialView['state'] = state
    let disabledKind: CredentialView['disabledKind']
    let disabledNote: string | undefined
    let coolingUntil: number | undefined
    let coolingForMs: number | undefined
    let lastError: string | undefined

    const roll = rnd()
    if (roll < disabledRatio) {
      st = 'disabled'
      disabledKind = pick(['INVALID_KEY', 'KEY_REVOKED', 'QUOTA_EXHAUSTED'] as const)
      disabledNote =
        disabledKind === 'INVALID_KEY'
          ? '401 连续 3 次，已弃用'
          : disabledKind === 'KEY_REVOKED'
            ? '403 连续 2 次，疑似吊销'
            : '响应声明额度耗尽'
    } else if (roll < disabledRatio + 0.14) {
      st = 'cooling'
      const remain = int(30, 400) * 1000
      coolingUntil = SEED_NOW + remain
      coolingForMs = remain
      lastError = '429 rate_limited'
    }

    const rpmLimit = chance(0.55) ? pick([60, 120, 300, 600]) : 0
    const tpmLimit = chance(0.4) ? pick([100_000, 200_000, 500_000]) : 0
    // TPM 未计量是常态：网关在发请求前拿不到 token 数，
    // 下游又没声明用量时只能显示「未计量」，绝不能显示成 0。
    const tpmMetered = tpmLimit > 0 && chance(0.55)

    const calls = int(200, 5200)
    const fails = Math.floor(calls * (st === 'cooling' ? 0.29 : st === 'disabled' ? 0.4 : rnd() * 0.02))

    out.push({
      id: `cred_${key}_${i + 1}`,
      label: credLabel(i),
      headerKeys: ['Authorization'],
      fingerprint: `…${Math.floor(rnd() * 0xffff).toString(16).padStart(4, '0')}`,
      configured: true,
      enabled: st !== 'disabled',
      state: st,
      disabledKind,
      disabledAt: disabledKind ? SEED_NOW - int(1, 20) * HOUR : undefined,
      disabledNote,
      coolingUntil,
      coolingForMs,
      consecutiveLimited: st === 'cooling' ? int(1, 5) : undefined,
      rpmLimit,
      tpmLimit,
      rpmUsed: st === 'active' ? Math.floor(rpmLimit * rnd() * 0.8) : 0,
      tpmUsed: tpmMetered ? Math.floor(tpmLimit * rnd() * 0.7) : 0,
      tpmMetered,
      windowEnd: SEED_NOW + int(5, 60) * 1000,
      picks: int(1, 900),
      lastUsedAt: SEED_NOW - int(5, 900) * 1000,
      lastError,
      callCount: calls,
      failCount: fails,
      successRate: (calls - fails) / calls,
      p95Ms: int(120, 2400),
      createdAt: SEED_NOW - int(30, 400) * DAY,
      updatedAt: SEED_NOW - int(1, 60) * HOUR,
    })
  }
  out[0].label = '主号'
  void prefix
  return out
}

function toolsFor(key: string, count: number): MockTool[] {
  const domain = DOMAIN_TOOLS[key] ?? []
  const names: string[] = []
  for (const n of domain) {
    if (names.length >= count) break
    names.push(n)
  }
  while (names.length < count) {
    const n = pick(GENERIC_TOOLS)
    if (!names.includes(n)) names.push(n)
    else names.push(`${n}_${names.length}`)
  }
  return names.map((name, idx) => {
    const calls = int(20, 5200)
    return {
      name,
      description: describeTool(key, name),
      inputSchema: makeSchema(name),
      // 设计稿里工具默认全启用，靠过滤名单控制是否暴露。
      enabled: true,
      callCount: calls,
      failCount: Math.floor(calls * rnd() * 0.03),
      ...(idx === -1 ? {} : {}),
    }
  })
}

function describeTool(serverKey: string, tool: string): string {
  const verb = tool.split('_')[0]
  const map: Record<string, string> = {
    search: '按关键词检索并返回匹配结果',
    fetch: '抓取指定资源并解析为结构化内容',
    list: '列出符合条件的全部条目',
    get: '读取单个对象的完整详情',
    create: '新建一条记录并返回其标识',
    update: '按字段增量更新已有记录',
    delete: '删除指定记录（不可撤销）',
    query: '按查询语句取回数据',
    translate: '将文本翻译为目标语言',
    generate: '按提示生成内容',
    read: '读取内容',
    write: '写入内容',
  }
  const tail = map[verb] ?? '执行该工具对应的下游动作'
  return `${serverKey} · ${tail}`
}

function buildServer(key: string, index: number): MockServer {
  const transport = index % 3 === 0 ? 'streamable_http' : index % 3 === 1 ? 'sse' : 'stdio'
  const toolCount = int(3, 15)
  const tools = toolsFor(key, toolCount)

  // 状态分布刻意做成「大部分健康、少量降级、个别离线」，
  // 与设计稿首屏的比例一致，方便验证筛选与徽标配色。
  const roll = rnd()
  let status: MockServer['status'] = 'healthy'
  if (roll < 0.12) status = 'offline'
  else if (roll < 0.34) status = 'degraded'
  else if (roll < 0.4) status = 'unknown'

  const enabled = !(status === 'unknown' && chance(0.5))
  const draining = enabled && chance(0.04)
  const legacy = chance(0.2)

  const credCount = chance(0.78) ? int(1, 5) : 0
  const creds = credCount ? makeCredentials(key, credCount, 0.18) : []

  const filterMode = chance(0.5) ? 'block' : chance(0.5) ? 'allow' : 'allow_all'
  let filterList: string[] = []
  if (filterMode !== 'allow_all') {
    filterList = tools
      .filter(() => chance(0.25))
      .map((t) => t.name)
    // allow + 空名单会被后端拒绝（工具会凭空消失），mock 同样不允许
    if (filterMode === 'allow' && filterList.length === 0) {
      filterList = [tools[0].name]
    }
  }

  // 分类是可空的单值 FK；未指定时归「未分类」，不阻塞接入。
  const cat = chance(0.8) ? MOCK_CATEGORIES[int(0, MOCK_CATEGORIES.length - 1)] : undefined

  return {
    id: `srv_${index + 1}`,
    key,
    name: key,
    transport,
    url:
      transport === 'stdio'
        ? undefined
        : transport === 'sse'
          ? `https://${key.replace(/-/g, '')}.example.com/sse`
          : `https://${key.replace(/-/g, '')}.example.com/mcp`,
    command: transport === 'stdio' ? 'npx' : undefined,
    args: transport === 'stdio' ? ['-y', `@mcp/server-${key}`] : undefined,
    env: transport === 'stdio' ? ['API_KEY=***'] : undefined,
    headers: transport === 'stdio' ? undefined : { Authorization: '***' },
    authMode: transport === 'stdio' ? 'none' : 'header',
    timeoutMs: pick([15000, 30000, 45000, 60000]),
    protocolVersion: legacy ? '2025-11-25' : '2026-07-28',
    legacyOnly: legacy,

    enabled,
    status: enabled ? status : 'unknown',
    draining,
    drainingInflight: draining ? int(1, 3) : undefined,
    drainingSince: draining ? SEED_NOW - int(2, 50) * 1000 : undefined,

    description: chance(0.6) ? `${key} 下游 · 由 ${transport} 接入` : undefined,
    tags: Array.from(new Set([pick(TAG_POOL), pick(TAG_POOL)])).slice(0, int(1, 2)),
    categoryId: cat?.id,
    categoryName: cat?.name,
    categorySlug: cat?.slug,

    toolFilterMode: filterMode,
    toolFilterList: filterList,

    estTokensPerCall: chance(0.35) ? pick([500, 1200, 3000]) : 0,

    protocolEra: legacy ? 'legacy' : 'modern',
    serverName: `mcp-${key}`,
    serverVersion: `${int(0, 3)}.${int(0, 9)}.${int(0, 20)}`,
    capabilities: ['tools', ...(chance(0.3) ? ['resources'] : []), ...(chance(0.2) ? ['prompts'] : [])],

    lastCheckAt: enabled ? SEED_NOW - int(5, 300) * 1000 : SEED_NOW - int(1, 40) * HOUR,
    lastLatencyMs: enabled ? (status === 'degraded' ? int(240, 900) : int(8, 180)) : undefined,
    lastErrorKind:
      enabled && status === 'offline'
        ? pick(['CONN_REFUSED', 'TIMEOUT', 'NOT_MCP'] as const)
        : enabled && status === 'degraded'
          ? pick(['RATE_LIMITED', 'UPSTREAM_ERROR', 'NO_USABLE_KEY'] as const)
          : undefined,
    lastError:
      enabled && status === 'offline'
        ? 'dial tcp 203.0.113.7:443: connect: connection refused'
        : enabled && status === 'degraded'
          ? 'HTTP 429 · Retry-After: 180s'
          : undefined,

    createdAt: SEED_NOW - int(2, 200) * DAY,
    updatedAt: SEED_NOW - int(1, 300) * HOUR,

    tools,
    creds,
  }
}

export const MOCK_CATEGORIES: Category[] = CATEGORY_POOL.map(([name, slug], i) => ({
  id: `cat_${i + 1}`,
  name,
  slug,
  description: `${name}相关的下游 server`,
  createdAt: SEED_NOW - 200 * DAY,
  updatedAt: SEED_NOW - 10 * DAY,
}))

/**
 * 内置的接入密钥（mock 内部约定）。
 *
 * 定义在 seed 而不是 db：日志 seed 也要拿它们做调用方归因，而 db
 * 反过来引用 seed。集中一份才不会出现"日志归因到一把清单里根本
 * 不存在的 key"——之前就是这么漂的（日志写 key_ci，清单里没有）。
 */
export const MOCK_API_KEYS = [
  {
    id: 'key_console',
    name: '管理后台',
    hex: '3f9c2a1b8d4e5f60',
    enabled: true,
    lastUsedAgoMs: 60_000,
    ageDays: 90,
  },
  {
    id: 'key_desktop',
    name: 'Claude Desktop · 我的 Mac',
    hex: '7b1e4d9a2c3f8e01',
    enabled: true,
    lastUsedAgoMs: 3_600_000,
    ageDays: 60,
  },
  // 停用态要留一条：否则"停用"这条路径在 mock 下永远看不到效果。
  {
    id: 'key_legacy',
    name: '旧接入方（已停用）',
    hex: 'c5a28f3d6b9e0c47',
    enabled: false,
    lastUsedAgoMs: 120 * DAY,
    ageDays: 120,
  },
] as const

export function createSeedServers(): MockServer[] {
  return SERVER_KEYS.map(buildServer)
}

/* ────────────────────────────── 日志 ────────────────────────────── */

export function createSeedLogs(servers: MockServer[], count = 600): LogItem[] {
  const out: LogItem[] = []
  let ts = SEED_NOW
  for (let i = 0; i < count; i++) {
    ts -= int(1, 240) * 1000
    const sv = pick(servers)
    const tool = pick(sv.tools)
    const failed = chance(0.14)
    const kind = failed ? pick(ERROR_KIND_POOL) : ''
    const cred = sv.creds.length ? pick(sv.creds) : undefined
    const isDraining = kind === 'SERVER_DRAINING'
    /* 七成调用带调用方，三成匿名 —— 后者对应"端点当时还没启用鉴权"。
       候选里含那把已停用的 key：它的**历史**日志仍在表里，而这正是
       名字快照要保住的场景（列表里已停用，历史行照样认得出是谁）。 */
    const caller = chance(0.7) ? pick(MOCK_API_KEYS) : undefined
    out.push({
      id: `log_${String(count - i).padStart(5, '0')}`,
      ts: new Date(ts).toISOString(),
      serverId: sv.id,
      serverKey: sv.key,
      toolName: tool.name,
      exposedName: exposedNameOf(sv, tool.name),
      durationMs: kind === 'TIMEOUT' || kind === 'CONN_REFUSED' ? 0 : int(6, 1800),
      status: failed ? 'error' : 'ok',
      errorKind: kind as LogItem['errorKind'],
      error: failed ? errorMessageOf(kind) : undefined,
      credentialId: cred?.id,
      apiKeyId: caller?.id,
      apiKeyName: caller?.name,
      args: JSON.stringify({ query: '示例入参', limit: 10 }),
      result: failed ? undefined : JSON.stringify({ items: int(1, 20), took: `${int(4, 300)}ms` }),
      ...(isDraining ? { error: '该 server 正在停用，已不接受新调用' } : {}),
    })
  }
  return out
}

function errorMessageOf(kind: string): string {
  switch (kind) {
    case 'TIMEOUT':
      return 'context deadline exceeded（已超过 server 超时上限）'
    case 'CONN_REFUSED':
      return 'dial tcp: connect: connection refused · 端口未监听'
    case 'RATE_LIMITED':
      return 'HTTP 429 · Retry-After: 180s · 上游限流'
    case 'NO_USABLE_KEY':
      return '该 server 的密钥池当前无可用密钥（1 把冷却中，2 把已弃用），约 30 秒后重试'
    case 'NOT_MCP':
      return '响应体不是 JSON-RPC，疑似填入了普通网站 URL'
    case 'AUTH_REQUIRED':
      return 'HTTP 401 · 凭据被下游拒绝'
    case 'UPSTREAM_ERROR':
      return 'HTTP 502 · 上游网关错误'
    case 'SERVER_DRAINING':
      return '目标正在停用，已不接受新调用'
    case 'CANCELED':
      return '收尾超时强制关闭，本次调用被中断'
    default:
      return '未知错误'
  }
}

export function createSeedKeyEvents(servers: MockServer[], count = 40): KeyEventItem[] {
  const out: KeyEventItem[] = []
  let ts = SEED_NOW
  for (let i = 0; i < count; i++) {
    ts -= int(2, 90) * MINUTE
    const sv = pick(servers.filter((s) => s.creds.length))
    if (!sv) continue
    const cred = pick(sv.creds)
    const type = pick(['KEY_COOLDOWN', 'KEY_DISABLED', 'KEY_RECOVERED'] as const)
    out.push({
      id: `ke_${i + 1}`,
      ts: new Date(ts).toISOString(),
      serverId: sv.id,
      serverKey: sv.key,
      type,
      credentialId: cred.id,
      credentialLabel: cred.label,
      detail:
        type === 'KEY_COOLDOWN'
          ? `RATE_LIMITED · Retry-After: ${int(30, 300)}s`
          : type === 'KEY_DISABLED'
            ? `${cred.disabledKind ?? 'INVALID_KEY'} · 连续 ${int(2, 3)} 次失败`
            : '冷却到期，恢复轮询',
    })
  }
  return out
}

/* ────────────────────────────── 视图折算 ────────────────────────────── */

export function toSummary(sv: MockServer): ServerSummary {
  const enabledTools = sv.tools.filter((t) => t.enabled).length
  const filtered = applyFilter(sv).length
  const toolCalls = sv.tools.reduce((a, t) => a + t.callCount, 0)
  const toolFails = sv.tools.reduce((a, t) => a + t.failCount, 0)

  const credCalls = sv.creds.reduce((a, c) => a + (c.callCount ?? 0), 0)
  const credFails = sv.creds.reduce((a, c) => a + (c.failCount ?? 0), 0)

  return {
    id: sv.id,
    key: sv.key,
    name: sv.name,
    transport: sv.transport,
    target: sv.transport === 'stdio' ? [sv.command, ...(sv.args ?? [])].join(' ') : (sv.url ?? ''),
    enabled: sv.enabled,
    status: sv.status,
    authMode: sv.authMode,
    protocolEra: sv.protocolEra,
    protocolVersion: sv.protocolVersion,
    serverName: sv.serverName,
    serverVersion: sv.serverVersion,
    capabilities: sv.capabilities,
    description: sv.description,
    tags: sv.tags,
    categoryId: sv.categoryId,
    category: sv.categoryName,
    toolCount: sv.tools.length,
    exposedToolCount: filtered,
    enabledToolCount: enabledTools,
    toolFilterMode: sv.toolFilterMode,
    toolFilterList: sv.toolFilterList,
    keyPool: sv.creds.length > 0,
    keyCount: sv.creds.length,
    usableKeyCount: sv.creds.filter((c) => c.enabled && c.state === 'active').length,
    coolingKeyCount: sv.creds.filter((c) => c.state === 'cooling').length,
    disabledKeyCount: sv.creds.filter((c) => c.state === 'disabled' || !c.enabled).length,
    keySuccessRate: credCalls ? (credCalls - credFails) / credCalls : undefined,
    estTokensPerCall: sv.estTokensPerCall,
    draining: sv.draining,
    drainingInflight: sv.drainingInflight,
    drainingSince: sv.drainingSince,
    toolSuccessRate: toolCalls ? (toolCalls - toolFails) / toolCalls : undefined,
    lastCheckAt: sv.lastCheckAt,
    lastLatencyMs: sv.lastLatencyMs,
    lastErrorKind: (sv.lastErrorKind ?? '') as ServerSummary['lastErrorKind'],
    lastError: sv.lastError,
    lastErrorHint: sv.lastErrorKind ? hintOf(sv.lastErrorKind) : '尚未探活',
    createdAt: sv.createdAt,
    updatedAt: sv.updatedAt,
  }
}

function hintOf(kind: string): string {
  const map: Record<string, string> = {
    TIMEOUT: '下游未在超时内响应，可上调超时或检查网络',
    CONN_REFUSED: '端口未监听，确认地址与端口是否正确',
    NOT_MCP: '返回的不是 MCP 响应，可能填入了普通网站 URL',
    RATE_LIMITED: '上游限流，网关会按密钥退避后自动恢复',
    NO_USABLE_KEY: '密钥池暂无可用的密钥，冷却到期后自动恢复',
    AUTH_REQUIRED: '凭据被拒绝，检查密钥是否正确或已过期',
    UPSTREAM_ERROR: '下游服务端错误，稍后重试',
  }
  return map[kind] ?? '查看调用日志获取详细错误'
}

/**
 * 对外工具名：`<分类英文名>__<serverKey>__<工具名>`。
 *
 * 与后端 `registry.ExposedToolName` 同规则。未分类的 server 前缀段
 * 固定为 uncategorized —— 分类段可以不完美，但不能缺席，
 * 否则工具名会变成「有时三段、有时两段」。
 *
 * 唯一性由 serverKey 的全局唯一约束保证（一个 server 只属一个分类），
 * 因此两个分类用同一个英文名也不会撞名，界面无需处理冲突。
 */
export function exposedNameOf(sv: MockServer, toolName: string): string {
  return `${sv.categorySlug ?? 'uncategorized'}__${sv.key}__${toolName}`
}

/** 过滤发生在聚合层：改名单立即生效，不必重新探活下游。 */
export function applyFilter(sv: MockServer): MockTool[] {
  if (sv.toolFilterMode === 'allow_all') return sv.tools.filter((t) => t.enabled)
  const list = new Set(sv.toolFilterList)
  const base = sv.tools.filter((t) => t.enabled)
  return sv.toolFilterMode === 'allow'
    ? base.filter((t) => list.has(t.name))
    : base.filter((t) => !list.has(t.name))
}

export function toToolEntries(servers: MockServer[]): ToolEntry[] {
  const out: ToolEntry[] = []
  for (const sv of servers) {
    if (!sv.enabled) continue
    for (const t of sv.tools) {
      out.push({
        exposedName: exposedNameOf(sv, t.name),
        name: t.name,
        serverId: sv.id,
        serverKey: sv.key,
        description: t.description,
        inputSchema: t.inputSchema,
        available: sv.status !== 'offline',
        callCount: t.callCount,
        successRate: (t.callCount - t.failCount) / t.callCount,
        enabled: applyFilter(sv).some((x) => x.name === t.name),
        category: sv.categoryName,
      })
    }
  }
  return out
}

export function toDetail(sv: MockServer): ServerDetail {
  return {
    ...toSummary(sv),
    config: {
      url: sv.url,
      headers: sv.headers,
      headerKeys: sv.headers ? Object.keys(sv.headers) : undefined,
      authMode: sv.authMode,
      command: sv.command,
      args: sv.args,
      env: sv.env,
      timeoutMs: sv.timeoutMs,
      protocolVersion: sv.protocolVersion,
      legacyOnly: sv.legacyOnly,
      estTokensPerCall: sv.estTokensPerCall,
    },
    tools: sv.tools.map((t) => t.name),
  }
}

/** 把请求体折回内部模型。语义是整体替换，不做字段级局部更新。 */
export function applyRequest(sv: MockServer, req: ServerRequest): void {
  sv.key = req.key.trim()
  sv.name = req.name.trim() || sv.key
  sv.transport = req.transport
  sv.url = req.url?.trim() || undefined
  sv.command = req.command?.trim() || undefined
  sv.args = req.args
  sv.env = req.env
  sv.timeoutMs = req.timeoutMs ?? sv.timeoutMs
  sv.protocolVersion = req.protocolVersion || sv.protocolVersion
  sv.legacyOnly = req.legacyOnly ?? false
  sv.authMode = req.authMode ?? sv.authMode
  sv.toolFilterMode = req.toolFilterMode ?? sv.toolFilterMode
  sv.toolFilterList = req.toolFilterList ?? sv.toolFilterList
  sv.estTokensPerCall = req.estTokensPerCall ?? 0
  sv.description = req.description || undefined
  sv.tags = req.tags ?? []
  sv.categoryId = req.categoryId || undefined
  const cat = MOCK_CATEGORIES.find((c) => c.id === sv.categoryId)
  sv.categoryName = cat?.name
  sv.categorySlug = cat?.slug
  if (req.enabled !== undefined) sv.enabled = req.enabled

  // 密钥只进不出：提交 *** 表示沿用原值。
  if (req.headers && Object.keys(req.headers).length) {
    const next: Record<string, string> = {}
    for (const [k, v] of Object.entries(req.headers)) {
      next[k] = v === '***' ? (sv.headers?.[k] ?? v) : v
    }
    sv.headers = next
  }
  sv.updatedAt = Date.now()
}

export const timeBases = { MINUTE, HOUR, DAY }
