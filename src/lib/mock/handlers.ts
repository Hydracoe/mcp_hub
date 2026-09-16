import { http, HttpResponse, delay } from 'msw'
import type {
  ApiErrorCode,
  Category,
  CredentialRequest,
  CredentialView,
  DisabledKind,
  EmbedConfig,
  KeyDetail,
  KeyEventItem,
  KeySummary,
  LogItem,
  ProbeLevel,
  ProbeResult,
  ServerDetail,
  ServerRequest,
  ToolEntry,
} from '@/lib/api/types'
import {
  SEED_NOW,
  applyFilter,
  applyRequest,
  exposedNameOf,
  toDetail,
  toSummary,
  toToolEntries,
  type MockServer,
} from './seed'
import { db, makeServerFromRequest, type ApiKeyModel } from './db'
import { normalizeCategorySlug } from '@/lib/slug'

/* ────────────────────────────── 基础设施 ────────────────────────────── */

/** 只有显式设置了 VITE_MOCK_API_KEY 才开启鉴权，默认放行，不打扰本地开发。 */
const EXPECTED_TOKEN = (import.meta.env.VITE_MOCK_API_KEY as string | undefined) ?? undefined

const rint = (min: number, max: number) => min + Math.floor(Math.random() * (max - min + 1))

function err(status: number, code: ApiErrorCode, message: string) {
  return HttpResponse.json({ code, error: message }, { status })
}

/** 校验 X-API-Key；未开启鉴权（无期望令牌）一律放行。返回 401 响应或 null。 */
function authGuard(req: Request): Response | null {
  if (!EXPECTED_TOKEN) return null
  const given = req.headers.get('X-API-Key')
  if (given !== EXPECTED_TOKEN) {
    return err(401, 'unauthorized', '缺少或错误的凭据')
  }
  return null
}

async function readJson<T>(req: Request): Promise<T | null> {
  try {
    const text = await req.text()
    if (!text) return null
    return JSON.parse(text) as T
  } catch {
    return null
  }
}

function qp(req: Request): URLSearchParams {
  return new URL(req.url).searchParams
}

function getServer(id: string): MockServer | undefined {
  return db.servers.find((s) => s.id === id)
}

/** draining 自然结束：超过 3 秒则 inflight 归零（PRD「先摘路由、再优雅退场」）。 */
function decayDraining(sv: MockServer): void {
  if (sv.draining && sv.drainingSince && Date.now() - sv.drainingSince > 3000) {
    sv.draining = false
    sv.drainingInflight = 0
    sv.drainingSince = undefined
  }
}

function serverKeySummary(sv: MockServer): KeySummary {
  const creds = sv.creds
  const usable = creds.filter((c) => c.enabled && c.state === 'active').length
  const cooling = creds.filter((c) => c.state === 'cooling').length
  const disabled = creds.filter((c) => c.state === 'disabled' || !c.enabled).length
  return {
    total: creds.length,
    servers: creds.length ? 1 : 0,
    usable,
    cooling,
    disabled,
    unusable: cooling + disabled,
  }
}

/* ────────────────────────────── 探活 / 试调 ────────────────────────────── */

/** 根据 server 当前状态构造 L1–L3 探活结果。离线/未知在 L1 即失败并附 stderr。 */
function buildProbe(sv: MockServer): ProbeResult {
  const offline = sv.status === 'offline'
  const unknown = sv.status === 'unknown'
  const isStdio = sv.transport === 'stdio'
  const levels: ProbeLevel[] = []

  const l1Ok = !offline && !unknown
  const l1: ProbeLevel = {
    level: 'L1',
    label: '可达性',
    ok: l1Ok,
    durationMs: rint(8, 180),
  }
  if (!l1Ok) {
    l1.errorKind = offline ? 'CONN_REFUSED' : 'UNKNOWN'
    l1.detail = offline
      ? 'dial tcp 203.0.113.7:443: connect: connection refused'
      : '该 server 尚未探活，状态未知'
    if (isStdio) {
      // §4.2.4：stdio 失败必须回传 stderr，这是定位问题的唯一线索
      l1.stderr = offline
        ? `npx: could not determine executable to run\nError: Cannot find module '@mcp/server-${sv.key}'\n    at resolve (node:internal/modules/cjs/loader:1)\n    at Module._resolveFilename (node:internal/modules/cjs/loader:2)`
        : "Error: spawnSync npx ENOENT\n    at ChildProcess.spawnSync"
    }
  }
  levels.push(l1)

  if (!l1Ok) {
    const totalMs = levels.reduce((a, l) => a + l.durationMs, 0)
    return {
      ok: false,
      protocolEra: sv.protocolEra,
      serverName: sv.serverName,
      levels,
      totalMs,
      errorKind: l1.errorKind,
      error: l1.detail,
      hint: offline ? '端口未监听，确认地址与端口是否正确' : '尚未探活，请手动触发一次测试',
    }
  }

  levels.push({
    level: 'L2',
    label: '协议协商',
    ok: true,
    durationMs: rint(4, 60),
    detail: `协商协议版本 ${sv.protocolVersion ?? '未知'}`,
  })
  levels.push({
    level: 'L3',
    label: '工具清单',
    ok: true,
    durationMs: rint(10, 120),
    detail: `列出 ${sv.tools.length} 个工具`,
  })

  const totalMs = levels.reduce((a, l) => a + l.durationMs, 0)
  return {
    ok: true,
    protocolVersion: sv.protocolVersion,
    protocolEra: sv.protocolEra,
    serverName: sv.serverName,
    serverVersion: sv.serverVersion,
    capabilities: sv.capabilities,
    toolCount: sv.tools.length,
    levels,
    totalMs,
  }
}

function probeToItem(sv: MockServer, probe: ProbeResult) {
  return {
    serverId: sv.id,
    serverKey: sv.key,
    name: sv.name,
    ok: probe.ok,
    enabled: sv.enabled,
    status: sv.status,
    latencyMs: probe.totalMs,
    protocolVersion: probe.protocolVersion,
    toolCount: probe.toolCount,
    errorKind: probe.errorKind ?? undefined,
    error: probe.error,
    hint: probe.hint,
  }
}

/* ────────────────────────────── 概览折算 ────────────────────────────── */

function computeCallStats() {
  const since = Date.now() - 24 * 60 * 60 * 1000
  const recent = db.logs.filter((l) => new Date(l.ts).getTime() >= since)
  const total = recent.length
  const failed = recent.filter((l) => l.status === 'error').length
  const durations = recent.map((l) => l.durationMs).filter((d) => d > 0)
  const avgMs = durations.length ? Math.round(durations.reduce((a, b) => a + b, 0) / durations.length) : 0
  const maxMs = durations.length ? Math.max(...durations) : 0
  return { total, failed, avgMs, maxMs, failureRate: total ? failed / total : 0 }
}

function computeToolStats() {
  let declared = 0
  let exposed = 0
  for (const sv of db.servers) {
    if (!sv.enabled) continue
    declared += sv.tools.length
    exposed += applyFilter(sv).length
  }
  const filtered = declared - exposed
  return { total: exposed, cached: declared, filtered, exposed }
}

function computeKeySummary(): KeySummary {
  let total = 0
  let servers = 0
  let usable = 0
  let cooling = 0
  let disabled = 0
  for (const sv of db.servers) {
    if (!sv.creds.length) continue
    total += sv.creds.length
    servers += 1
    usable += sv.creds.filter((c) => c.enabled && c.state === 'active').length
    cooling += sv.creds.filter((c) => c.state === 'cooling').length
    disabled += sv.creds.filter((c) => c.state === 'disabled' || !c.enabled).length
  }
  return {
    total,
    servers,
    usable,
    cooling,
    disabled,
    unusable: cooling + disabled,
  }
}

/** 缺口 B3：每把不可用密钥的运行时明细，供 overview「密钥池状态明细」段使用。 */
function buildKeyDetails(): KeyDetail[] {
  const out: KeyDetail[] = []
  for (const sv of db.servers) {
    for (const c of sv.creds) {
      if (c.state === 'active') continue
      const detail: KeyDetail = {
        serverId: sv.id,
        serverKey: sv.key,
        credentialId: c.id,
        label: c.label,
        state: c.state,
      }
      if (c.state === 'cooling') {
        const remain = c.coolingUntil ? Math.max(0, c.coolingUntil - Date.now()) : 0
        detail.coolingRemainingMs = remain
        detail.lastError = c.lastError
      } else {
        detail.disabledKind = c.disabledKind as DisabledKind | undefined
        detail.disabledNote = c.disabledNote
        detail.lastError = c.lastError
      }
      out.push(detail)
    }
  }
  return out
}

/* ────────────────────────────── 工具检索（M7） ────────────────────────────── */

/** 分词：小写 ASCII 词 + 中文 bigram（CJK 无词边界，PRD §4.7.4 必需项）。 */
function tokenize(text: string): string[] {
  const lower = (text ?? '').toLowerCase()
  const tokens: string[] = []
  const ascii = lower.match(/[a-z0-9]+/g) ?? []
  tokens.push(...ascii)
  const cjkRuns = lower.match(/[一-鿿]+/g) ?? []
  for (const run of cjkRuns) {
    for (let i = 0; i + 1 < run.length; i++) tokens.push(run.slice(i, i + 2))
  }
  return tokens
}

interface SearchDoc {
  entry: ToolEntry
  tags: string[]
  serverDesc: string
}

function buildSearchCorpus(): SearchDoc[] {
  const docs: SearchDoc[] = []
  for (const sv of db.servers) {
    if (!sv.enabled) continue
    if (sv.status === 'offline' || sv.status === 'unknown') continue
    for (const t of applyFilter(sv)) {
      docs.push({
        entry: {
          exposedName: exposedNameOf(sv, t.name),
          name: t.name,
          serverId: sv.id,
          serverKey: sv.key,
          description: t.description,
          inputSchema: t.inputSchema,
          available: true,
          category: sv.categoryName,
        },
        tags: sv.tags,
        serverDesc: sv.description ?? sv.serverName ?? '',
      })
    }
  }
  return docs
}

function weightedTokens(d: SearchDoc): string[] {
  const toks: string[] = []
  toks.push(...tokenize(d.entry.name).flatMap((t) => Array(3).fill(t)))
  toks.push(...tokenize(d.tags.join(' ')).flatMap((t) => Array(2).fill(t)))
  toks.push(...tokenize(d.entry.description ?? ''))
  toks.push(...tokenize(d.serverDesc))
  return toks
}

function runSearch(q: string, mode: 'keyword' | 'bm25' | 'hybrid', k: number) {
  const docs = buildSearchCorpus()
  const ql = q.toLowerCase()

  if (mode === 'keyword') {
    const matched = docs
      .filter((d) => {
        const hay = `${d.entry.exposedName} ${d.entry.name} ${d.entry.description ?? ''} ${d.tags.join(' ')} ${d.serverDesc}`.toLowerCase()
        return hay.includes(ql)
      })
      .map((d) => d.entry)
    return { total: matched.length, items: matched, degraded: undefined as string | undefined }
  }

  // bm25 / hybrid 共用 BM25 打分
  const N = docs.length
  const df = new Map<string, number>()
  const docToks = docs.map((d) => {
    const toks = weightedTokens(d)
    for (const t of new Set(toks)) df.set(t, (df.get(t) ?? 0) + 1)
    return toks
  })
  const idf = (t: string) => Math.log((N - (df.get(t) ?? 0) + 0.5) / ((df.get(t) ?? 0) + 0.5)) + 1
  const qToks = tokenize(q)
  const scored = docs
    .map((d, i) => {
      const tf = new Map<string, number>()
      for (const t of docToks[i]) tf.set(t, (tf.get(t) ?? 0) + 1)
      let score = 0
      for (const qt of qToks) {
        const f = tf.get(qt)
        if (f) score += idf(qt) * f
      }
      return { entry: d.entry, score }
    })
    .filter((x) => x.score > 0)
    .sort((a, b) => b.score - a.score)

  const items = scored.slice(0, k).map((x) => x.entry)
  const degraded = mode === 'hybrid' ? 'vector_unavailable' : undefined
  return { total: items.length, items, degraded }
}

/* ────────────────────────────── 密钥池工具 ────────────────────────────── */

function applyCredentialRequest(_sv: MockServer, old: CredentialView | null, req: CredentialRequest, id: string): CredentialView {
  let headerKeys = old?.headerKeys
  let fingerprint = old?.fingerprint ?? `…${rint(0, 0xffff).toString(16).padStart(4, '0')}`

  const headersGiven = req.headers && Object.keys(req.headers).length > 0
  const allStars = headersGiven && Object.values(req.headers!).every((v) => v === '***')

  if (headersGiven && !allStars) {
    headerKeys = Object.keys(req.headers!)
  } else if (req.value && req.value !== '***' && req.authScheme) {
    const hname =
      req.headerName ??
      (req.authScheme === 'bearer' ? 'Authorization' : req.authScheme === 'x-api-key' ? 'X-API-Key' : 'X-Custom-Header')
    headerKeys = [hname]
    fingerprint = `…${req.value.slice(-4)}`
  }

  const label = req.label?.trim() || old?.label || '新密钥'
  return {
    id,
    label,
    headerKeys,
    fingerprint,
    configured: true,
    enabled: old ? old.enabled : true,
    state: old ? old.state : 'active',
    disabledKind: old?.disabledKind,
    disabledAt: old?.disabledAt,
    disabledNote: old?.disabledNote,
    coolingUntil: old?.coolingUntil,
    coolingForMs: old?.coolingForMs,
    consecutiveLimited: old?.consecutiveLimited,
    rpmLimit: req.rpmLimit ?? old?.rpmLimit ?? 0,
    tpmLimit: req.tpmLimit ?? old?.tpmLimit ?? 0,
    rpmUsed: old?.rpmUsed ?? 0,
    tpmUsed: old?.tpmUsed ?? 0,
    tpmMetered: (req.tpmLimit ?? old?.tpmLimit ?? 0) > 0,
    windowEnd: old?.windowEnd,
    picks: old?.picks ?? 0,
    lastUsedAt: old?.lastUsedAt,
    lastError: old?.lastError,
    callCount: old?.callCount ?? 0,
    failCount: old?.failCount ?? 0,
    successRate: old?.successRate,
    p95Ms: old?.p95Ms,
    createdAt: old?.createdAt ?? Date.now(),
    updatedAt: Date.now(),
  }
}

function nextCredentialLabel(sv: MockServer, base: string): string {
  if (!sv.creds.some((c) => c.label === base)) return base
  let i = 2
  while (sv.creds.some((c) => c.label === `${base} (${i})`)) i++
  return `${base} (${i})`
}

/* ─────────────────────── 接入 API Key ─────────────────────── */

/**
 * 接入密钥的对外形态：不含明文，时间统一毫秒。
 *
 * 与 Go 端 apikeyItem 逐字段对齐 —— mock 若在这里多给几个字段
 * （历史上给过 callCount），界面就会照着开发，接真后端才发现是空的。
 */
function toApiKeyView(k: ApiKeyModel) {
  return {
    id: k.id,
    name: k.name,
    prefix: k.prefix,
    enabled: k.enabled,
    lastUsedAt: k.lastUsedAt,
    createdAt: k.createdAt,
  }
}

/** 生成 `mh-` + 32 位 hex，等价于 Go 端 rand 16 字节。 */
function newApiKeySecret(): string {
  const bytes = new Uint8Array(16)
  crypto.getRandomValues(bytes)
  return `mh-${Array.from(bytes, (b) => b.toString(16).padStart(2, '0')).join('')}`
}

/** 启用 / 停用的公共路径。后端返回 204，无响应体。 */
async function toggleApiKey(req: Request, id: string, enabled: boolean): Promise<Response> {
  const a = authGuard(req)
  if (a) return a
  await delay(rint(200, 500))
  const k = db.apiKeys.find((x) => x.id === id)
  if (!k) return err(404, 'not_found', '目标不存在')
  k.enabled = enabled
  return new HttpResponse(null, { status: 204 })
}

/* ────────────────────────────── 主流程 ────────────────────────────── */

export const handlers = [
  /* GET /api/overview — 实时折算 + 缺口 B3 的 keyDetails（读接口，不校验 X-API-Key） */
  http.get('/api/overview', async () => {
    await delay(rint(80, 320))
    for (const sv of db.servers) decayDraining(sv)
    const servers = {
      total: db.servers.length,
      enabled: db.servers.filter((s) => s.enabled).length,
      healthy: 0,
      degraded: 0,
      offline: 0,
      unknown: 0,
    }
    for (const sv of db.servers) servers[sv.status]++
    return HttpResponse.json({
      ready: true,
      builtAt: SEED_NOW,
      uptimeSeconds: Math.floor((Date.now() - SEED_NOW) / 1000),
      version: 'v1.0.0',
      servers,
      tools: computeToolStats(),
      calls: computeCallStats(),
      credentials: computeKeySummary(),
      endpoints: {
        mcp: '/mcp',
        health: '/healthz',
        ready: '/readyz',
        admin: '/api',
        sse: '/sse',
      },
      keyDetails: buildKeyDetails(),
    })
  }),

  /* GET /api/connect — 接入指引（读接口，不校验） */
  http.get('/api/connect', async () => {
    await delay(rint(80, 320))
    const host = 'http://localhost:8080'
    return HttpResponse.json({
      mcpEndpoint: `${host}/mcp`,
      sseEndpoint: `${host}/sse`,
      healthEndpoint: `${host}/healthz`,
      mcpServers: { 'mcp-hub': host },
      toolCount: db.servers.reduce((a, s) => a + applyFilter(s).length, 0),
      serverCount: db.servers.length,
      summary: '将上述配置粘贴到任意 MCP 客户端即可接入聚合后的全部工具。',
      note: '管理接口需携带 X-API-Key（若网关以 --api-key 启动）。',
    })
  }),

  /* POST /api/servers/test-all — 全量测试，停用的单列 disabled 不计入成败（PRD §7.3 修过的 bug） */
  http.post('/api/servers/test-all', async ({ request }) => {
    const a = authGuard(request)
    if (a) return a
    await delay(rint(200, 500))
    const started = Date.now()
    const results = []
    let disabled = 0
    for (const sv of db.servers) {
      if (!sv.enabled) {
        disabled++
        continue
      }
      const probe = buildProbe(sv)
      results.push(probeToItem(sv, probe))
    }
    const healthy = results.filter((r) => r.ok).length
    const failed = results.length - healthy
    return HttpResponse.json({
      total: results.length,
      healthy,
      failed,
      disabled,
      tookMs: Date.now() - started,
      results,
      testedAt: Date.now(),
    })
  }),

  /* GET /api/servers — 列表（用 seed 的 toSummary） */
  http.get('/api/servers', async ({ request }) => {
    const a = authGuard(request)
    if (a) return a
    await delay(rint(80, 320))
    for (const sv of db.servers) decayDraining(sv)
    const items = db.servers.map((sv) => toSummary(sv))
    return HttpResponse.json({ total: items.length, items })
  }),

  /* POST /api/servers — 新增；?probe=1 附 probe，?upsert=1 允许覆盖同名（FR-1.3 静默清空工具拦截） */
  http.post('/api/servers', async ({ request }) => {
    const a = authGuard(request)
    if (a) return a
    await delay(rint(200, 500))
    const req = await readJson<ServerRequest>(request)
    if (!req || !req.key || !/^[a-z0-9][a-z0-9_-]*$/.test(req.key.trim())) {
      return err(400, 'invalid_request', 'key 必填，且仅允许小写字母、数字、-、_，且以字母/数字开头')
    }
    const upsert = qp(request).get('upsert') === '1'
    const existing = db.servers.find((s) => s.key === req.key.trim())
    if (existing && !upsert) {
      return err(409, 'key_taken', `server key "${req.key.trim()}" 已存在`)
    }
    if (req.transport === 'streamable_http' || req.transport === 'sse') {
      if (!req.url || !req.url.trim()) return err(400, 'invalid_request', 'streamable_http / sse 传输需要 url')
    }
    if (req.transport === 'stdio') {
      if (!req.command || !req.command.trim()) return err(400, 'invalid_request', 'stdio 传输需要 command')
    }
    if (req.toolFilterMode === 'allow' && (!req.toolFilterList || req.toolFilterList.length === 0)) {
      return err(400, 'invalid_request', 'allow 模式下 toolFilterList 不能为空（否则工具会被静默清空）')
    }
    // 新建必须指定分类，且分类必须真实存在（与后端同规则）。
    // 编辑（?upsert=1 命中已有 server）不在此限，沿用整体替换语义。
    if (!existing) {
      if (!req.categoryId?.trim()) {
        return err(400, 'invalid_request', '新建 server 必须指定分类')
      }
      if (!db.categories.some((c) => c.id === req.categoryId)) {
        return err(400, 'invalid_request', '分类不存在')
      }
    }
    let sv: MockServer
    if (existing) {
      applyRequest(existing, req)
      sv = existing
    } else {
      sv = makeServerFromRequest(req, `srv_${db.servers.length + 1}`)
      db.servers.push(sv)
    }
    const probe = qp(request).get('probe') === '1' ? buildProbe(sv) : undefined
    const detail = toDetail(sv) as ServerDetail
    detail.credentials = serverKeySummary(sv)
    detail.probe = probe
    return HttpResponse.json(detail)
  }),

  /* POST /api/servers/:id/test — 单点 L1–L3 探活，离线/stdio 失败演示 */
  http.post('/api/servers/:id/test', async ({ request, params }) => {
    const a = authGuard(request)
    if (a) return a
    await delay(rint(200, 500))
    const sv = getServer((params as { id: string }).id)
    if (!sv) return err(404, 'not_found', 'server 不存在')
    return HttpResponse.json(buildProbe(sv))
  }),

  /* POST /api/servers/:id/refresh-tools — 刷新工具缓存（不改启用状态） */
  http.post('/api/servers/:id/refresh-tools', async ({ request, params }) => {
    const a = authGuard(request)
    if (a) return a
    await delay(rint(200, 500))
    const sv = getServer((params as { id: string }).id)
    if (!sv) return err(404, 'not_found', 'server 不存在')
    return HttpResponse.json(buildProbe(sv))
  }),

  /* POST /api/servers/:id/enable — 启用 */
  http.post('/api/servers/:id/enable', async ({ request, params }) => {
    const a = authGuard(request)
    if (a) return a
    await delay(rint(200, 500))
    const sv = getServer((params as { id: string }).id)
    if (!sv) return err(404, 'not_found', 'server 不存在')
    sv.enabled = true
    sv.status = sv.status === 'unknown' ? 'healthy' : sv.status
    sv.draining = false
    sv.drainingInflight = 0
    sv.drainingSince = undefined
    const detail = toDetail(sv) as ServerDetail
    detail.credentials = serverKeySummary(sv)
    return HttpResponse.json(detail)
  }),

  /* POST /api/servers/:id/disable — 停用进入 draining，3 秒后自然结束 */
  http.post('/api/servers/:id/disable', async ({ request, params }) => {
    const a = authGuard(request)
    if (a) return a
    await delay(rint(200, 500))
    const sv = getServer((params as { id: string }).id)
    if (!sv) return err(404, 'not_found', 'server 不存在')
    sv.enabled = false
    sv.status = 'unknown'
    sv.draining = true
    sv.drainingInflight = rint(1, 3)
    sv.drainingSince = Date.now()
    const detail = toDetail(sv) as ServerDetail
    detail.credentials = serverKeySummary(sv)
    return HttpResponse.json(detail)
  }),

  /* POST /api/servers/:id/credentials/import — 批量粘贴，一行一把 */
  http.post('/api/servers/:id/credentials/import', async ({ request, params }) => {
    const a = authGuard(request)
    if (a) return a
    await delay(rint(200, 500))
    const sv = getServer((params as { id: string }).id)
    if (!sv) return err(404, 'not_found', 'server 不存在')
    if (sv.transport === 'stdio') return err(400, 'key_pool_unsupported', 'stdio 下游的凭据在子进程环境变量里，改它需要重启进程，密钥池不可用')
    const body = await readJson<{ lines?: string[]; authScheme?: 'bearer' | 'x-api-key' | 'header'; headerName?: string; rpmLimit?: number; tpmLimit?: number }>(request)
    const lines = body?.lines ?? []
    const created: CredentialView[] = []
    for (const raw of lines) {
      const line = raw.trim()
      if (!line) continue
      let label = ''
      let value = line
      const eq = line.indexOf('=')
      if (eq > 0) {
        label = line.slice(0, eq).trim()
        value = line.slice(eq + 1).trim()
      }
      const id = `cred_${sv.key}_${sv.creds.length + 1}_${Date.now()}`
      const req: CredentialRequest = {
        label: nextCredentialLabel(sv, label || '粘贴密钥'),
        value,
        authScheme: body?.authScheme ?? 'bearer',
        headerName: body?.headerName,
        rpmLimit: body?.rpmLimit,
        tpmLimit: body?.tpmLimit,
      }
      const cred = applyCredentialRequest(sv, null, req, id)
      sv.creds.push(cred)
      created.push(cred)
    }
    return HttpResponse.json({ created: created.length, items: created })
  }),

  /* GET /api/servers/:id/credentials — 列表 + keyPool 标志 */
  http.get('/api/servers/:id/credentials', async ({ request, params }) => {
    const a = authGuard(request)
    if (a) return a
    await delay(rint(80, 320))
    const sv = getServer((params as { id: string }).id)
    if (!sv) return err(404, 'not_found', 'server 不存在')
    if (sv.transport === 'stdio') return err(400, 'key_pool_unsupported', 'stdio 下游的凭据在子进程环境变量里，改它需要重启进程，密钥池不可用')
    return HttpResponse.json({ total: sv.creds.length, items: sv.creds, keyPool: sv.creds.length > 0 })
  }),

  /* POST /api/servers/:id/credentials — 新增一把密钥（统一追加到末尾，不采用调用方 sort_order） */
  http.post('/api/servers/:id/credentials', async ({ request, params }) => {
    const a = authGuard(request)
    if (a) return a
    await delay(rint(200, 500))
    const sv = getServer((params as { id: string }).id)
    if (!sv) return err(404, 'not_found', 'server 不存在')
    if (sv.transport === 'stdio') return err(400, 'key_pool_unsupported', 'stdio 下游的凭据在子进程环境变量里，改它需要重启进程，密钥池不可用')
    const req = await readJson<CredentialRequest>(request)
    if (!req) return err(400, 'invalid_request', '缺少请求体')
    const id = `cred_${sv.key}_${sv.creds.length + 1}_${Date.now()}`
    if (req.label) req.label = nextCredentialLabel(sv, req.label)
    const cred = applyCredentialRequest(sv, null, req, id)
    sv.creds.push(cred)
    return HttpResponse.json(cred)
  }),

  /* POST /api/servers/:id/credentials/:cid/enable — 人工启用 */
  http.post('/api/servers/:id/credentials/:cid/enable', async ({ request, params }) => {
    const a = authGuard(request)
    if (a) return a
    await delay(rint(200, 500))
    const { id, cid } = params as { id: string; cid: string }
    const sv = getServer(id)
    if (!sv) return err(404, 'not_found', 'server 不存在')
    if (sv.transport === 'stdio') return err(400, 'key_pool_unsupported', 'stdio 下游不支持密钥池')
    const cred = sv.creds.find((c) => c.id === cid)
    if (!cred) return err(404, 'not_found', '密钥不存在')
    cred.enabled = true
    cred.state = 'active'
    cred.disabledKind = undefined
    cred.disabledNote = undefined
    cred.updatedAt = Date.now()
    return HttpResponse.json(cred)
  }),

  /* POST /api/servers/:id/credentials/:cid/disable — 人工停用 */
  http.post('/api/servers/:id/credentials/:cid/disable', async ({ request, params }) => {
    const a = authGuard(request)
    if (a) return a
    await delay(rint(200, 500))
    const { id, cid } = params as { id: string; cid: string }
    const sv = getServer(id)
    if (!sv) return err(404, 'not_found', 'server 不存在')
    if (sv.transport === 'stdio') return err(400, 'key_pool_unsupported', 'stdio 下游不支持密钥池')
    const cred = sv.creds.find((c) => c.id === cid)
    if (!cred) return err(404, 'not_found', '密钥不存在')
    cred.enabled = false
    cred.state = 'disabled'
    cred.disabledKind = 'INVALID_KEY'
    cred.disabledNote = '人工停用'
    cred.disabledAt = Date.now()
    cred.updatedAt = Date.now()
    return HttpResponse.json(cred)
  }),

  /* POST /api/servers/:id/credentials/:cid/test — 单密钥只读探活 */
  http.post('/api/servers/:id/credentials/:cid/test', async ({ request, params }) => {
    const a = authGuard(request)
    if (a) return a
    await delay(rint(200, 500))
    const { id, cid } = params as { id: string; cid: string }
    const sv = getServer(id)
    if (!sv) return err(404, 'not_found', 'server 不存在')
    if (sv.transport === 'stdio') return err(400, 'key_pool_unsupported', 'stdio 下游不支持密钥池')
    const cred = sv.creds.find((c) => c.id === cid)
    if (!cred) return err(404, 'not_found', '密钥不存在')
    return HttpResponse.json(buildProbe(sv))
  }),

  /* POST /api/servers/:id/credentials/:cid/reset-quota — 清零本地配额计数 */
  http.post('/api/servers/:id/credentials/:cid/reset-quota', async ({ request, params }) => {
    const a = authGuard(request)
    if (a) return a
    await delay(rint(200, 500))
    const { id, cid } = params as { id: string; cid: string }
    const sv = getServer(id)
    if (!sv) return err(404, 'not_found', 'server 不存在')
    if (sv.transport === 'stdio') return err(400, 'key_pool_unsupported', 'stdio 下游不支持密钥池')
    const cred = sv.creds.find((c) => c.id === cid)
    if (!cred) return err(404, 'not_found', '密钥不存在')
    cred.rpmUsed = 0
    cred.tpmUsed = 0
    cred.consecutiveLimited = 0
    cred.updatedAt = Date.now()
    return HttpResponse.json(cred)
  }),

  /* PUT /api/servers/:id/credentials/:cid — 编辑；值为 *** 表示沿用原值（§7.5.13 缺陷 3） */
  http.put('/api/servers/:id/credentials/:cid', async ({ request, params }) => {
    const a = authGuard(request)
    if (a) return a
    await delay(rint(200, 500))
    const { id, cid } = params as { id: string; cid: string }
    const sv = getServer(id)
    if (!sv) return err(404, 'not_found', 'server 不存在')
    if (sv.transport === 'stdio') return err(400, 'key_pool_unsupported', 'stdio 下游不支持密钥池')
    const cred = sv.creds.find((c) => c.id === cid)
    if (!cred) return err(404, 'not_found', '密钥不存在')
    const req = await readJson<CredentialRequest>(request)
    if (!req) return err(400, 'invalid_request', '缺少请求体')
    const updated = applyCredentialRequest(sv, cred, req, cid)
    const idx = sv.creds.findIndex((c) => c.id === cid)
    sv.creds[idx] = updated
    return HttpResponse.json(updated)
  }),

  /* DELETE /api/servers/:id/credentials/:cid — 删除，204 无体 */
  http.delete('/api/servers/:id/credentials/:cid', async ({ request, params }) => {
    const a = authGuard(request)
    if (a) return a
    await delay(rint(200, 500))
    const { id, cid } = params as { id: string; cid: string }
    const sv = getServer(id)
    if (!sv) return err(404, 'not_found', 'server 不存在')
    if (sv.transport === 'stdio') return err(400, 'key_pool_unsupported', 'stdio 下游不支持密钥池')
    const idx = sv.creds.findIndex((c) => c.id === cid)
    if (idx < 0) return err(404, 'not_found', '密钥不存在')
    sv.creds.splice(idx, 1)
    return new HttpResponse(null, { status: 204 })
  }),

  /* GET /api/servers/:id — 详情（toDetail + 附密钥池汇总） */
  http.get('/api/servers/:id', async ({ request, params }) => {
    const a = authGuard(request)
    if (a) return a
    await delay(rint(80, 320))
    const sv = getServer((params as { id: string }).id)
    if (!sv) return err(404, 'not_found', 'server 不存在')
    decayDraining(sv)
    const detail = toDetail(sv) as ServerDetail
    detail.credentials = serverKeySummary(sv)
    return HttpResponse.json(detail)
  }),

  /* PUT /api/servers/:id — 编辑（整体替换；*** 沿用）。?probe=1 附 probe */
  http.put('/api/servers/:id', async ({ request, params }) => {
    const a = authGuard(request)
    if (a) return a
    await delay(rint(200, 500))
    const sv = getServer((params as { id: string }).id)
    if (!sv) return err(404, 'not_found', 'server 不存在')
    const req = await readJson<ServerRequest>(request)
    if (!req) return err(400, 'invalid_request', '缺少请求体')
    applyRequest(sv, req)
    const probe = qp(request).get('probe') === '1' ? buildProbe(sv) : undefined
    const detail = toDetail(sv) as ServerDetail
    detail.credentials = serverKeySummary(sv)
    detail.probe = probe
    return HttpResponse.json(detail)
  }),

  /* DELETE /api/servers/:id */
  http.delete('/api/servers/:id', async ({ request, params }) => {
    const a = authGuard(request)
    if (a) return a
    await delay(rint(200, 500))
    const sv = getServer((params as { id: string }).id)
    if (!sv) return err(404, 'not_found', 'server 不存在')
    db.servers = db.servers.filter((s) => s.id !== sv.id)
    return new HttpResponse(null, { status: 204 })
  }),

  /* GET /api/tools/search — M7 简易检索：keyword / bm25 / hybrid（hybrid 降级 vector_unavailable） */
  http.get('/api/tools/search', async ({ request }) => {
    const a = authGuard(request)
    if (a) return a
    await delay(rint(80, 320))
    const sp = qp(request)
    const q = sp.get('q') ?? ''
    const mode = (sp.get('mode') as 'keyword' | 'bm25' | 'hybrid') ?? 'keyword'
    const category = sp.get('category')
    const server = sp.get('server')
    const tag = sp.get('tag')
    const k = sp.get('k') ? Number(sp.get('k')) : 5
    let { total, items, degraded } = runSearch(q, mode, k || 5)
    if (category || server || tag) {
      items = items.filter(
        (t) =>
          (!category || t.category === category) &&
          (!server || t.serverKey === server) &&
          (!tag || db.servers.find((s) => s.id === t.serverId)?.tags.includes(tag)),
      )
      total = items.length
    }
    return HttpResponse.json({ total, items, degraded })
  }),

  /* GET /api/tools — 聚合工具列表，支持 serverKey / q */
  http.get('/api/tools', async ({ request }) => {
    const a = authGuard(request)
    if (a) return a
    await delay(rint(80, 320))
    const sp = qp(request)
    const serverKey = sp.get('serverKey')
    const q = (sp.get('q') ?? '').toLowerCase()
    let items = toToolEntries(db.servers)
    if (serverKey) items = items.filter((t) => t.serverKey === serverKey)
    if (q) {
      items = items.filter((t) =>
        [t.exposedName, t.description ?? '', t.serverKey].some((f) => f.toLowerCase().includes(q)),
      )
    }
    return HttpResponse.json({ total: items.length, items })
  }),

  /* 缺口 B1：GET /api/logs/key-events — 密钥事件流，真实后端无此端点，mock 是唯一数据源 */
  http.get('/api/logs/key-events', async ({ request }) => {
    const a = authGuard(request)
    if (a) return a
    await delay(rint(80, 320))
    const sp = qp(request)
    const serverId = sp.get('serverId')
    const limit = sp.get('limit') ? Number(sp.get('limit')) : 100
    let items: KeyEventItem[] = db.keyEvents
    if (serverId) items = items.filter((e) => e.serverId === serverId)
    items = items.slice(0, limit || 100)
    return HttpResponse.json({ total: items.length, items })
  }),

  /* DELETE /api/logs — 清空日志 */
  http.delete('/api/logs', async ({ request }) => {
    const a = authGuard(request)
    if (a) return a
    await delay(rint(200, 500))
    const deleted = db.logs.length
    db.logs = []
    return HttpResponse.json({ deleted })
  }),

  /* GET /api/logs — 调用日志，支持多维筛选与分页 */
  http.get('/api/logs', async ({ request }) => {
    const a = authGuard(request)
    if (a) return a
    await delay(rint(80, 320))
    const sp = qp(request)
    const serverId = sp.get('serverId')
    const apiKeyId = sp.get('apiKeyId')
    const status = sp.get('status')
    const kind = sp.get('kind')
    const q = (sp.get('q') ?? '').trim().toLowerCase()
    const since = sp.get('since') ? Number(sp.get('since')) : undefined
    const until = sp.get('until') ? Number(sp.get('until')) : undefined
    const limit = sp.get('limit') ? Number(sp.get('limit')) : 50
    const offset = sp.get('offset') ? Number(sp.get('offset')) : 0
    const sort = sp.get('sort') ?? 'ts'
    const order = sp.get('order') ?? 'desc'

    let rows = [...db.logs]
    if (serverId) rows = rows.filter((l) => l.serverId === serverId)
    /* 按调用方筛。保留值 anonymous → 比空串：空串在这里表示"不筛"，
       所以要"只看匿名"必须另有一个词，与后端 store.LogCallerAnonymous 一致。 */
    if (apiKeyId) {
      const want = apiKeyId === 'anonymous' ? '' : apiKeyId
      rows = rows.filter((l) => (l.apiKeyId ?? '') === want)
    }
    if (status) rows = rows.filter((l) => l.status === status)
    if (kind) rows = rows.filter((l) => l.errorKind === kind)
    if (since) rows = rows.filter((l) => new Date(l.ts).getTime() >= since!)
    if (until) rows = rows.filter((l) => new Date(l.ts).getTime() <= until!)
    /* 自由文本搜索。字段清单与后端 store.logSearchClause 一一对应：只搜
       "身份"（谁调的 / 调的谁 / 哪个工具 / 哪把下游密钥），不搜 args / result。

       includes() 是字面子串匹配，正好对上后端转义过的 LIKE —— 那边把 % 和 _
       转成字面量，就是为了让"搜 100%"找的是 100% 本身而不是"以 100 开头"。 */
    if (q) {
      rows = rows.filter((l) =>
        [l.toolName, l.exposedName, l.serverKey, l.credentialId ?? '', l.apiKeyName ?? ''].some((f) =>
          f.toLowerCase().includes(q),
        ),
      )
    }

    /* 排序白名单与后端 store.logSortColumns 一一对应。列名要拼进 ORDER BY，
       所以不能把请求里的字符串直接当排序键用；不在表里的值回退到 ts。

       后两级键是稳定性所需：耗时 / 状态有大量相同值，ts 也会撞（同一毫秒
       落库多条），没有兜底键的话同一页内的先后顺序在两次查询间会抖动，
       翻页时漏行或重复。 */
    type SortKey = 'ts' | 'durationMs' | 'status'
    const cmpKey: SortKey =
      sort === 'durationMs' || sort === 'status' ? sort : 'ts'
    const desc = order !== 'asc'
    const primaryOf = (l: LogItem) =>
      cmpKey === 'durationMs' ? l.durationMs : cmpKey === 'status' ? l.status : new Date(l.ts).getTime()

    rows.sort((x, y) => {
      const a = primaryOf(x)
      const b = primaryOf(y)
      const primary = typeof a === 'string' && typeof b === 'string' ? a.localeCompare(b) : Number(a) - Number(b)
      if (primary !== 0) return desc ? -primary : primary
      const byTime = new Date(y.ts).getTime() - new Date(x.ts).getTime()
      return byTime !== 0 ? byTime : x.id.localeCompare(y.id)
    })

    const total = rows.length
    const items = rows.slice(offset, offset + (limit || 50))
    return HttpResponse.json({ total, limit: limit || 50, offset: offset || 0, items })
  }),

  /* GET /api/categories + POST /api/categories */
  http.get('/api/categories', async ({ request }) => {
    const a = authGuard(request)
    if (a) return a
    await delay(rint(80, 320))
    const items = db.categories.map((c) => withCategoryCounts(c))
    return HttpResponse.json({ total: items.length, items })
  }),

  http.post('/api/categories', async ({ request }) => {
    const a = authGuard(request)
    if (a) return a
    await delay(rint(200, 500))
    const body = await readJson<{ name?: string; slug?: string; description?: string }>(request)
    if (!body?.name?.trim()) return err(400, 'invalid_request', '中文名不能为空')
    const slug = normalizeCategorySlug(body.slug ?? '')
    // 英文名必须是 [a-z0-9-]，这是它进工具名前缀段的硬前提。
    if (!slug) return err(400, 'invalid_request', '英文名不能为空，且至少包含一个字母或数字')
    // 中文名与英文名各自判重：两个分类叫同一个名字（无论哪种语言）
    // 都会让按分类过滤时命中两份语义。
    if (db.categories.some((c) => c.name === body.name!.trim())) {
      return err(409, 'category_taken', `分类 "${body.name.trim()}" 已存在`)
    }
    if (db.categories.some((c) => c.slug === slug)) {
      return err(409, 'category_taken', `分类英文名 "${slug}" 已被占用`)
    }
    const cat: Category = {
      id: `cat_${db.categories.length + 1}`,
      name: body.name.trim(),
      slug,
      description: body.description ?? '',
      createdAt: Date.now(),
      updatedAt: Date.now(),
    }
    db.categories.push(cat)
    return HttpResponse.json(cat)
  }),

  /* PUT /api/categories/:id + DELETE /api/categories/:id（删除需校验无 server 引用） */
  http.put('/api/categories/:id', async ({ request, params }) => {
    const a = authGuard(request)
    if (a) return a
    await delay(rint(200, 500))
    const cat = db.categories.find((c) => c.id === (params as { id: string }).id)
    if (!cat) return err(404, 'not_found', '分类不存在')
    const body = await readJson<{ name?: string; slug?: string; description?: string }>(request)
    if (!body) return err(400, 'invalid_request', '缺少请求体')
    const newName = body.name?.trim()
    if (newName && newName !== cat.name && db.categories.some((c) => c.name === newName)) {
      return err(409, 'category_taken', `分类 "${newName}" 已存在`)
    }
    const newSlug = normalizeCategorySlug(body.slug ?? cat.slug)
    if (!newSlug) return err(400, 'invalid_request', '英文名不能为空，且至少包含一个字母或数字')
    if (newSlug !== cat.slug && db.categories.some((c) => c.slug === newSlug && c.id !== cat.id)) {
      return err(409, 'category_taken', `分类英文名 "${newSlug}" 已被占用`)
    }
    if (newName) cat.name = newName
    cat.slug = newSlug
    if (body.description !== undefined) cat.description = body.description
    cat.updatedAt = Date.now()
    // 改英文名会改掉该分类下所有 server 的工具名，同步刷新它们的缓存字段。
    for (const sv of db.servers) {
      if (sv.categoryId === cat.id) {
        sv.categoryName = cat.name
        sv.categorySlug = cat.slug
      }
    }
    return HttpResponse.json(withCategoryCounts(cat))
  }),

  http.delete('/api/categories/:id', async ({ request, params }) => {
    const a = authGuard(request)
    if (a) return a
    await delay(rint(200, 500))
    const cat = db.categories.find((c) => c.id === (params as { id: string }).id)
    if (!cat) return err(404, 'not_found', '分类不存在')
    // 与后端一致：引用它的 server 先摘出（category_id 置空）再删分类。
    // 分类没有外键约束，不先清引用就会留下悬空 id；而拦着不让删
    // 会把「我想解散这个分类」变成一件需要先逐台改 server 的苦差。
    for (const sv of db.servers) {
      if (sv.categoryId === cat.id) {
        sv.categoryId = undefined
        sv.categoryName = undefined
        sv.categorySlug = undefined
      }
    }
    db.categories = db.categories.filter((c) => c.id !== cat.id)
    return new HttpResponse(null, { status: 204 })
  }),

  /* ── 接入 API Key（上游 AI 客户端调 /mcp 时出示的凭据） ── */

  /* GET /api/keys — 清单，不含明文 */
  http.get('/api/keys', async ({ request }) => {
    const a = authGuard(request)
    if (a) return a
    await delay(rint(80, 320))
    const items = db.apiKeys.map(toApiKeyView)
    return HttpResponse.json({ total: items.length, items })
  }),

  /* POST /api/keys — 创建。明文 key 只在这一次响应里交付。 */
  http.post('/api/keys', async ({ request }) => {
    const a = authGuard(request)
    if (a) return a
    await delay(rint(200, 500))
    const body = await readJson<{ name?: string }>(request)
    const name = body?.name?.trim() ?? ''
    if (!name) return err(400, 'invalid_request', 'api key 需要一个 name 来标识调用方')

    const secret = newApiKeySecret()
    const model: ApiKeyModel = {
      id: `key_${Date.now()}`,
      name,
      secret,
      prefix: secret.slice(0, 10),
      enabled: true,
      lastUsedAt: 0,
      createdAt: Date.now(),
    }
    // 新建的排最前：后端按 created_at 倒序，这里保持同一读法。
    db.apiKeys.unshift(model)
    return HttpResponse.json({ ...toApiKeyView(model), key: secret }, { status: 201 })
  }),

  /* DELETE /api/keys/:id — 删除即失效：此后用它访问 /mcp 一律 401 */
  http.delete('/api/keys/:id', async ({ request, params }) => {
    const a = authGuard(request)
    if (a) return a
    await delay(rint(200, 500))
    const id = (params as { id: string }).id
    if (!db.apiKeys.some((x) => x.id === id)) return err(404, 'not_found', '目标不存在')
    db.apiKeys = db.apiKeys.filter((x) => x.id !== id)
    return new HttpResponse(null, { status: 204 })
  }),

  /* POST /api/keys/:id/enable */
  http.post('/api/keys/:id/enable', async ({ request, params }) =>
    toggleApiKey(request, (params as { id: string }).id, true),
  ),

  /* POST /api/keys/:id/disable — 停用是收紧，不会让端点退回匿名开放 */
  http.post('/api/keys/:id/disable', async ({ request, params }) =>
    toggleApiKey(request, (params as { id: string }).id, false),
  ),

  /* GET /api/embed-config + PUT /api/embed-config */
  http.get('/api/embed-config', async ({ request }) => {
    const a = authGuard(request)
    if (a) return a
    await delay(rint(80, 320))
    return HttpResponse.json(db.embed.config)
  }),

  http.put('/api/embed-config', async ({ request }) => {
    const a = authGuard(request)
    if (a) return a
    await delay(rint(200, 500))
    const body = await readJson<Partial<EmbedConfig>>(request)
    if (!body) return err(400, 'invalid_request', '缺少请求体')
    db.embed.config = { ...db.embed.config, ...body, configured: true }
    return HttpResponse.json(db.embed.config)
  }),

  /* GET /api/embed-config/rebuild + POST（每次 GET 推进进度，6 次内跑完，进度条可演示） */
  http.get('/api/embed-config/rebuild', async ({ request }) => {
    const a = authGuard(request)
    if (a) return a
    await delay(rint(80, 320))
    const st = db.embed.rebuild
    if (st.running) {
      const step = Math.max(1, Math.ceil(st.total / 6))
      st.done = Math.min(st.total, st.done + step)
      if (st.done >= st.total) {
        st.running = false
        st.finishedAt = Date.now()
      }
    }
    return HttpResponse.json({ ...st })
  }),

  http.post('/api/embed-config/rebuild', async ({ request }) => {
    const a = authGuard(request)
    if (a) return a
    await delay(rint(200, 500))
    db.embed.rebuild = {
      running: true,
      total: 120,
      done: 0,
      failed: 0,
      startedAt: Date.now(),
      finishedAt: undefined,
    }
    return HttpResponse.json({ ...db.embed.rebuild })
  }),
]

/* ────────────────────────────── 本地工具函数 ────────────────────────────── */

function withCategoryCounts(c: Category): Category {
  const servers = db.servers.filter((s) => s.categoryId === c.id)
  return { ...c, serverCount: servers.length, toolCount: servers.reduce((a, s) => a + applyFilter(s).length, 0) }
}
