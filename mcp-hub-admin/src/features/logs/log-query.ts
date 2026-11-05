import {
  DEFAULT_LOG_ORDER,
  DEFAULT_LOG_SORT,
  LOG_SORTS,
  type ErrorKind,
  type LogOrder,
  type LogQuery,
  type LogSort,
} from '@/lib/api/types'

/** 筛选栏的值。`q` 用空串表示"没有搜索词"，其余用 undefined 表示"不筛"。 */
export interface LogFilterValues {
  serverId?: string
  /** 调用方（上游接入密钥）的 id；LOG_CALLER_ANONYMOUS 表示只看匿名调用。 */
  apiKeyId?: string
  status?: 'ok' | 'error'
  kind?: string
  q: string
  since?: number
  until?: number
}

/** 命中任一键即视为"换了筛选条件"，此时页码要打回第 1 页。 */
export const LOG_FILTER_KEYS = [
  'serverId',
  'apiKeyId',
  'status',
  'kind',
  'q',
  'since',
  'until',
] as const

/** 点未激活列时先给该列最有信息量的一端：时间最近、耗时最慢、状态失败排前。 */
export const FIRST_DIR: Record<LogSort, LogOrder> = {
  ts: 'desc',
  durationMs: 'desc',
  // 'error' < 'ok'，所以升序把失败排前面
  status: 'asc',
}

/** 读一个数字型查询参数。非法值回退到 `fallback`，不让 NaN 渗进请求体。 */
const readInt = (params: URLSearchParams, key: string, fallback: number): number => {
  const raw = params.get(key)
  if (!raw) return fallback
  const n = Number(raw)
  return Number.isFinite(n) ? Math.max(1, Math.trunc(n)) : fallback
}

const readNum = (params: URLSearchParams, key: string): number | undefined => {
  const raw = params.get(key)
  if (!raw) return undefined
  const n = Number(raw)
  return Number.isFinite(n) ? n : undefined
}

/** 从 URL 读筛选值；脏值（手改或旧链接）回退默认，避免界面箭头与后端不一致。 */
export const readFilterValues = (params: URLSearchParams): LogFilterValues => ({
  serverId: params.get('serverId') ?? undefined,
  apiKeyId: params.get('apiKeyId') ?? undefined,
  status: (params.get('status') as 'ok' | 'error' | null) ?? undefined,
  kind: params.get('kind') ?? undefined,
  q: params.get('q') ?? '',
  since: readNum(params, 'since'),
  until: readNum(params, 'until'),
})

export const readSort = (params: URLSearchParams): LogSort => {
  const raw = params.get('sort')
  return (LOG_SORTS as readonly string[]).includes(raw ?? '')
    ? (raw as LogSort)
    : DEFAULT_LOG_SORT
}

export const readOrder = (params: URLSearchParams): LogOrder =>
  (params.get('order') ?? '').toLowerCase() === 'asc' ? 'asc' : DEFAULT_LOG_ORDER

export const readPage = (params: URLSearchParams): number => readInt(params, 'page', 1)

export const readPageSize = (params: URLSearchParams, fallback: number): number =>
  readInt(params, 'size', fallback)

/** 把 URL 状态与视图状态拼成请求体。必须在渲染期 memo，否则 identity 每次变化都会重拉。 */
export const toLogQuery = (args: {
  values: LogFilterValues
  sort: LogSort
  order: LogOrder
  page: number
  size: number
}): LogQuery => {
  const { values, sort, order, page, size } = args
  return {
    serverId: values.serverId,
    apiKeyId: values.apiKeyId,
    status: values.status,
    kind: (values.kind || undefined) as ErrorKind | undefined,
    q: values.q || undefined,
    since: values.since,
    until: values.until,
    sort,
    order,
    limit: size,
    offset: (page - 1) * size,
  }
}
