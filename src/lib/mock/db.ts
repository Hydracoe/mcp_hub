import type {
  Category,
  EmbedConfig,
  EmbedRebuildStatus,
  KeyEventItem,
  LogItem,
  ServerRequest,
} from '@/lib/api/types'
import {
  MOCK_API_KEYS,
  MOCK_CATEGORIES,
  createSeedKeyEvents,
  createSeedLogs,
  createSeedServers,
  SEED_NOW,
  type MockServer,
} from './seed'

/**
 * 内存数据库：模块加载时由 seed 生成，所有 handler 共享同一个 `db` 引用。
 * 写操作直接 mutate `db`；`resetDb()` 用全新 seed 覆盖，便于测试复位。
 */

/** 接入 API Key 的内部模型（明文只在创建响应里回一次，见 ApiKeyView）。 */
export interface ApiKeyModel {
  id: string
  name: string
  /** 完整明文。只在创建响应里交付一次，列表接口不输出。 */
  secret: string
  /** 明文前 10 位（`mh-` + 7 位 hex），与 Go 端 `plaintext[:10]` 对齐。 */
  prefix: string
  enabled: boolean
  /** Unix 毫秒；0 表示从未被使用过。 */
  lastUsedAt: number
  createdAt: number
}

export interface EmbedState {
  config: EmbedConfig
  rebuild: EmbedRebuildStatus
}

export interface Db {
  servers: MockServer[]
  logs: LogItem[]
  keyEvents: KeyEventItem[]
  categories: Category[]
  apiKeys: ApiKeyModel[]
  embed: EmbedState
}

/** 为新建/导入的 server 生成若干占位工具（不与 seed 内部耦合）。 */
export function makeToolsFor(key: string, count: number): MockServer['tools'] {
  const verbs = ['search', 'fetch', 'list', 'get', 'query']
  const names = Array.from({ length: Math.max(1, count) }, (_, i) => verbs[i % verbs.length])
  return names.map((n) => ({
    name: n,
    description: `${key} · 执行 ${n} 动作`,
    inputSchema: {
      type: 'object',
      properties: { query: { type: 'string', description: '查询内容' } },
      required: ['query'],
    },
    enabled: true,
    callCount: 0,
    failCount: 0,
  }))
}

/** 从 ServerRequest 折出一个全新的 MockServer（用于创建 / 导入）。 */
export function makeServerFromRequest(req: ServerRequest, id: string): MockServer {
  const now = Date.now()
  const transport = req.transport
  const sv: MockServer = {
    id,
    key: req.key.trim(),
    name: req.name.trim() || req.key.trim(),
    transport,
    url: transport === 'stdio' ? undefined : req.url?.trim() || undefined,
    command: transport === 'stdio' ? req.command?.trim() || 'npx' : undefined,
    args: transport === 'stdio' ? req.args : undefined,
    env: transport === 'stdio' ? req.env : undefined,
    headers: transport === 'stdio' ? undefined : req.headers,
    authMode: req.authMode ?? (transport === 'stdio' ? 'none' : 'header'),
    timeoutMs: req.timeoutMs ?? 30000,
    protocolVersion: req.protocolVersion || '2026-07-28',
    legacyOnly: req.legacyOnly ?? false,

    enabled: req.enabled ?? true,
    status: 'healthy',
    draining: false,

    description: req.description || undefined,
    tags: req.tags ?? [],
    categoryId: req.categoryId || undefined,
    categoryName: MOCK_CATEGORIES.find((c) => c.id === req.categoryId)?.name,
    categorySlug: MOCK_CATEGORIES.find((c) => c.id === req.categoryId)?.slug,

    toolFilterMode: req.toolFilterMode ?? 'allow_all',
    toolFilterList: req.toolFilterList ?? [],

    estTokensPerCall: req.estTokensPerCall ?? 0,

    protocolEra: req.legacyOnly ? 'legacy' : 'modern',
    serverName: `mcp-${req.key.trim()}`,
    serverVersion: '0.0.1',
    capabilities: ['tools'],

    createdAt: now,
    updatedAt: now,

    tools: makeToolsFor(req.key.trim(), 5),
    creds: [],
  }
  sv.status = sv.enabled ? 'healthy' : 'unknown'
  return sv
}

/** 由 MOCK_API_KEYS 补齐明文与前缀。明文只在创建响应里交付，见 ApiKeyView。 */
function makeApiKeys(): ApiKeyModel[] {
  return MOCK_API_KEYS.map((k) => {
    const secret = `mh-${k.hex}`
    return {
      id: k.id,
      name: k.name,
      secret,
      prefix: secret.slice(0, 10),
      enabled: k.enabled,
      lastUsedAt: SEED_NOW - k.lastUsedAgoMs,
      createdAt: SEED_NOW - k.ageDays * 86_400_000,
    }
  })
}

function makeEmbed(): EmbedState {
  return {
    config: {
      baseUrl: '',
      model: '',
      dims: 0,
      rerankEnabled: false,
      corpusFields: ['工具描述', 'instructions'],
      retrieveTopN: 20,
      configured: false,
    },
    rebuild: { running: false, total: 0, done: 0, failed: 0 },
  }
}

export function createDb(): Db {
  const servers = createSeedServers()
  const logs = createSeedLogs(servers)
  const keyEvents = createSeedKeyEvents(servers)
  const categories = MOCK_CATEGORIES.map((c) => ({ ...c }))
  return {
    servers,
    logs,
    keyEvents,
    categories,
    apiKeys: makeApiKeys(),
    embed: makeEmbed(),
  }
}

export const db: Db = createDb()

/** 复位为全新 seed（测试 / 登出场景使用）。 */
export function resetDb(): void {
  const fresh = createDb()
  db.servers = fresh.servers
  db.logs = fresh.logs
  db.keyEvents = fresh.keyEvents
  db.categories = fresh.categories
  db.apiKeys = fresh.apiKeys
  db.embed = fresh.embed
}
