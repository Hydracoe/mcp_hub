import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'

import * as api from '../endpoints'
import { POLL, queryKeys } from '../query-keys'
import type { ServerRequest } from '../types'

export function useServers() {
  return useQuery({
    queryKey: queryKeys.servers,
    queryFn: api.listServers,
    refetchInterval: POLL.servers,
  })
}

export function useServer(id: string | undefined) {
  return useQuery({
    queryKey: queryKeys.server(id ?? ''),
    queryFn: () => api.getServer(id as string),
    enabled: Boolean(id),
  })
}

function useRefreshAfterServerChange() {
  const qc = useQueryClient()
  return (serverId?: string) => {
    void qc.invalidateQueries({ queryKey: queryKeys.servers })
    void qc.invalidateQueries({ queryKey: queryKeys.overview })
    void qc.invalidateQueries({ queryKey: ['tools'] })
    if (serverId) void qc.invalidateQueries({ queryKey: queryKeys.server(serverId) })
  }
}

export function useCreateServer() {
  const refresh = useRefreshAfterServerChange()
  return useMutation({
    mutationFn: (vars: { body: ServerRequest; probe?: boolean; upsert?: boolean }) =>
      api.createServer(vars.body, { probe: vars.probe, upsert: vars.upsert }),
    onSuccess: (data) => refresh(data?.id),
  })
}

export function useUpdateServer(id: string) {
  const refresh = useRefreshAfterServerChange()
  return useMutation({
    mutationFn: (body: ServerRequest) => api.updateServer(id, body),
    onSuccess: () => refresh(id),
  })
}

export function useDeleteServer() {
  const refresh = useRefreshAfterServerChange()
  return useMutation({
    mutationFn: (id: string) => api.deleteServer(id),
    onSuccess: () => refresh(),
  })
}

export function useSetServerEnabled() {
  const refresh = useRefreshAfterServerChange()
  return useMutation({
    mutationFn: (vars: { id: string; enabled: boolean }) =>
      api.setServerEnabled(vars.id, vars.enabled),
    onSuccess: (_d, vars) => refresh(vars.id),
  })
}

export function useTestServer() {
  const refresh = useRefreshAfterServerChange()
  return useMutation({
    mutationFn: (id: string) => api.testServer(id),
    onSuccess: (_d, id) => refresh(id),
  })
}

export function useTestAllServers() {
  const refresh = useRefreshAfterServerChange()
  const qc = useQueryClient()
  return useMutation({
    mutationFn: () => api.testAllServers(),
    onSuccess: (data) => {
      // 后端不保存最近一次全量测试报告，前端内存留一份即可。
      qc.setQueryData(['servers', 'test-all'], data)
      refresh()
    },
  })
}

export function useRefreshTools() {
  const refresh = useRefreshAfterServerChange()
  return useMutation({
    mutationFn: (id: string) => api.refreshServerTools(id),
    onSuccess: (_d, id) => refresh(id),
  })
}
