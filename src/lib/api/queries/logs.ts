import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'

import * as api from '../endpoints'
import { POLL, queryKeys } from '../query-keys'
import type { LogQuery } from '../types'

export function useLogs(query: LogQuery, enabled = true) {
  return useQuery({
    queryKey: queryKeys.logs(query),
    queryFn: () => api.listLogs(query),
    enabled,
    refetchInterval: POLL.logs,
    // 翻页/换筛选时保留上一页数据，避免表格整块闪白。
    placeholderData: (prev) => prev,
  })
}

export function useKeyEvents(serverId?: string, enabled = true) {
  return useQuery({
    queryKey: queryKeys.keyEvents(serverId),
    queryFn: () => api.listKeyEvents({ serverId, limit: 100 }),
    enabled,
    refetchInterval: POLL.logs,
    // 后端可能还没有这个端点（404）。
    // 让它安静失败，由界面降级成「该视图暂未开放」，不要反复重试。
    retry: false,
  })
}

export function useClearLogs() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: () => api.clearLogs(),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['logs'] })
      void qc.invalidateQueries({ queryKey: queryKeys.overview })
    },
  })
}
