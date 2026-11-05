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

export type BackendConnection = 'connecting' | 'connected' | 'disconnected'

/**
 * 管理界面文案的「是否可用」= 前端能否连上后端，与 `overview.ready`（是否有工具可提供）是两件事：
 * 空库时 ready=false 属正常待接入，不是故障。壳层徽标一律用此状态，ready 只出现在 tooltip。
 * 判据复用 overview 查询（按 queryKey 复用，不额外发请求）。
 */
export function useBackendConnection(): BackendConnection {
  const { isError, isSuccess } = useOverview()
  if (isError) return 'disconnected'
  if (isSuccess) return 'connected'
  return 'connecting'
}
