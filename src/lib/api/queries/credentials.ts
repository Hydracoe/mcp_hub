import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'

import * as api from '../endpoints'
import { POLL, queryKeys } from '../query-keys'
import type { CredentialRequest } from '../types'

export function useCredentials(serverId: string | undefined, enabled = true) {
  return useQuery({
    queryKey: queryKeys.credentials(serverId ?? ''),
    queryFn: () => api.listCredentials(serverId as string),
    enabled: Boolean(serverId) && enabled,
    refetchInterval: POLL.credentials,
  })
}

/** 密钥池的全部写操作。它们在同一个 hook 里是因为共享同一份失效范围。 */
export function useCredentialMutations(serverId: string) {
  const qc = useQueryClient()
  const invalidate = () => {
    void qc.invalidateQueries({ queryKey: queryKeys.credentials(serverId) })
    void qc.invalidateQueries({ queryKey: queryKeys.server(serverId) })
    void qc.invalidateQueries({ queryKey: queryKeys.servers })
    void qc.invalidateQueries({ queryKey: queryKeys.overview })
  }

  const create = useMutation({
    mutationFn: (body: CredentialRequest) => api.createCredential(serverId, body),
    onSuccess: invalidate,
  })
  const update = useMutation({
    mutationFn: (vars: { cid: string; body: CredentialRequest }) =>
      api.updateCredential(serverId, vars.cid, vars.body),
    onSuccess: invalidate,
  })
  const remove = useMutation({
    mutationFn: (cid: string) => api.deleteCredential(serverId, cid),
    onSuccess: invalidate,
  })
  const setEnabled = useMutation({
    mutationFn: (vars: { cid: string; enabled: boolean }) =>
      api.setCredentialEnabled(serverId, vars.cid, vars.enabled),
    onSuccess: invalidate,
  })
  const test = useMutation({
    mutationFn: (cid: string) => api.testCredential(serverId, cid),
  })
  const resetQuota = useMutation({
    mutationFn: (cid: string) => api.resetCredentialQuota(serverId, cid),
    onSuccess: invalidate,
  })
  const importLines = useMutation({
    mutationFn: (vars: {
      lines: string[]
      authScheme?: 'bearer' | 'x-api-key' | 'header'
      headerName?: string
      rpmLimit?: number
      tpmLimit?: number
    }) => api.importCredentials(serverId, vars),
    onSuccess: invalidate,
  })

  return { create, update, remove, setEnabled, test, resetQuota, importLines, invalidate }
}
