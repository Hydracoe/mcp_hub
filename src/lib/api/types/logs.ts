import type { ErrorKind } from './common'

export interface LogItem {
  id: string
  /** RFC3339。 */
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
  /** 调用方名字的快照（调用时那把密钥叫什么）。改名或删除都不影响历史行。 */
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

/** 可排序的列，键与后端白名单一一对应。传表里没有的值会静默回退到 `ts`，加列要两边一起改。 */
export const LOG_SORTS = ['ts', 'durationMs', 'status'] as const

export type LogSort = (typeof LOG_SORTS)[number]

export type LogOrder = 'asc' | 'desc'

export const DEFAULT_LOG_SORT: LogSort = 'ts'
export const DEFAULT_LOG_ORDER: LogOrder = 'desc'

/** 「只看匿名」的保留值，必须与后端 `store.LogCallerAnonymous` 一致（空串=不筛）。 */
export const LOG_CALLER_ANONYMOUS = 'anonymous'

export interface LogQuery {
  serverId?: string
  status?: 'ok' | 'error'
  kind?: ErrorKind
  /** 按调用方（接入密钥）筛，可用 `LOG_CALLER_ANONYMOUS` 只看匿名。 */
  apiKeyId?: string
  q?: string
  since?: number
  until?: number
  /** 排序列与方向，默认 `ts` / `desc`（最新在前）。 */
  sort?: LogSort
  order?: LogOrder
  limit?: number
  offset?: number
}

/** 密钥事件。后端目前只写 slog，这里先按前端内存流呈现。 */
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
