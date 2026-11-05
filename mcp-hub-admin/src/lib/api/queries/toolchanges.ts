import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'

import * as api from '../endpoints'
import { queryKeys } from '../query-keys'

/* ────────────────────────────── 查询 ────────────────────────────── */

export function useToolChanges(params?: { serverId?: string; limit?: number }) {
  const serverId = params?.serverId
  const limit = params?.limit ?? 50
  return useQuery({
    queryKey: queryKeys.toolChanges(serverId, limit),
    queryFn: () => api.listToolChanges({ serverId, limit }),
  })
}

/* ────────────────────────────── 变更 ────────────────────────────── */

/** 单工具启用 / 禁用。
 *  后端响应整份 server 详情，直接灌进详情查询缓存做到即时反馈；
 *  其余依赖（聚合工具列表、server 列表、概览、变更记录）统一失效。 */
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
