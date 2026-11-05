import type { LogQuery } from './types'

/** 查询键集中管理：`invalidateQueries` 靠前缀批量失效，散落的字符串写错一个字母会静默失效。 */
export const queryKeys = {
  overview: ['overview'] as const,
  connect: ['connect'] as const,
  servers: ['servers'] as const,
  server: (id: string) => ['servers', id] as const,
  credentials: (serverId: string) => ['servers', serverId, 'credentials'] as const,
  tools: (params?: { serverKey?: string; q?: string }) => ['tools', params ?? {}] as const,
  toolSearch: (q: string, mode: string) => ['tools', 'search', q, mode] as const,
  toolChanges: (serverId?: string, limit = 50) =>
    ['tool-changes', serverId ?? '', limit] as const,
  logs: (query: LogQuery) => ['logs', query] as const,
  keyEvents: (serverId?: string) => ['logs', 'key-events', serverId ?? ''] as const,
  categories: ['categories'] as const,
  apiKeys: ['keys'] as const,
}

/** 轮询节奏（毫秒）。集中在此因为轮询密度是全局性能决策，分散后看不出整站请求量。 */
export const POLL = {
  overview: 15_000,
  servers: 20_000,
  logs: 5_000,
  credentials: 10_000,
} as const
