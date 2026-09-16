import type { LogQuery } from './types'

/** 查询键集中在一处：便于 `invalidateQueries` 用前缀批量失效，散布的字符串写错一个字母会静默失效（症状「改完界面没变」）。 */
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

/** 轮询节奏（毫秒）：概览 15s、日志 5s。集中在此而非各 hook：轮询密度是全局性能决策，分散后没人看得出整站每秒打几请求。 */
export const POLL = {
  overview: 15_000,
  servers: 20_000,
  logs: 5_000,
  credentials: 10_000,
} as const
