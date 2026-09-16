import { useQuery, type UseQueryOptions } from '@tanstack/react-query'

import * as api from '../endpoints'
import { POLL, queryKeys } from '../query-keys'

export function useOverview(
  options?: Partial<UseQueryOptions<Awaited<ReturnType<typeof api.getOverview>>>>,
) {
  return useQuery({
    queryKey: queryKeys.overview,
    queryFn: api.getOverview,
    refetchInterval: POLL.overview,
    ...options,
  })
}

export function useConnect() {
  return useQuery({ queryKey: queryKeys.connect, queryFn: api.getConnect })
}

/* ────────────────────────────── 连接状态 ────────────────────────────── */

/** 后端连接状态。 */
export type BackendConnection = 'connecting' | 'connected' | 'disconnected'

/**
 * 管理界面文案里的「是否可用」= 前端能不能连上后端，与后端的
 * `overview.ready`（判据是"有工具可提供"）是两件事：
 *
 * - **连接状态**回答"服务是不是活的"。连上了就说明部署通了，界面可正常使用。
 * - **网关就绪**回答"有没有工具可提供"。空库（还没接任何下游）时为 false，
 *   但那是"等你来接 Server"，不是故障——拿它当界面状态会天天误报。
 *
 * 所以壳层（工具栏徽标 / 侧边栏状态）一律用这个，`overview.ready` 只作为
 * 详情信息出现在 tooltip 里。
 *
 * 判据用 overview 这一次查询：react-query 按 queryKey 复用，同一页多次调用
 * 不会多发请求；它又是壳层本来就依赖的数据，不引入额外探针。
 */
export function useBackendConnection(): BackendConnection {
  const { isError, isSuccess } = useOverview()
  if (isError) return 'disconnected'
  if (isSuccess) return 'connected'
  return 'connecting'
}
