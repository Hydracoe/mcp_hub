import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'

import * as api from '../endpoints'
import { queryKeys } from '../query-keys'

export function useToolChanges(params?: { serverId?: string; limit?: number }) {
  const serverId = params?.serverId
  const limit = params?.limit ?? 50
  return useQuery({
    queryKey: queryKeys.toolChanges(serverId, limit),
    queryFn: () => api.listToolChanges({ serverId, limit }),
  })
}

/** 单工具启用 / 禁用：响应含整份 server 详情，直接灌入详情缓存做即时反馈；其余依赖统一失效。 */
export function useSetToolEnabled() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (vars: { serverId: string; toolName: string; enabled: boolean }) =>
      api.setToolEnabled(vars.serverId, vars.toolName, vars.enabled),
    onSuccess: (detail, vars) => {
      qc.setQueryData(queryKeys.server(vars.serverId), detail)
      void qc.invalidateQueries({ queryKey: ['tool-changes'] })
      void qc.invalidateQueries({ queryKey: ['tools'] })
      void qc.invalidateQueries({ queryKey: queryKeys.servers })
      void qc.invalidateQueries({ queryKey: queryKeys.overview })
    },
  })
}
