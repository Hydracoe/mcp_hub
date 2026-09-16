import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'

import * as api from '../endpoints'
import { queryKeys } from '../query-keys'

/** 接入密钥清单。刻意不轮询：表的变化只来自本页操作（每个都会主动失效它），
 *  否则用户正对着"刚创建、还没复制"的密钥时列表会突然刷新。 */
export function useApiKeys() {
  return useQuery({ queryKey: queryKeys.apiKeys, queryFn: api.listApiKeys })
}

export function useApiKeyMutations() {
  const qc = useQueryClient()
  // 创建后 total 从 0 变 1，会同时改变 /mcp 的鉴权状态（零 key 开放
  // → 有 key 强制），所以接入指引页也要跟着重取。
  const invalidate = () => {
    void qc.invalidateQueries({ queryKey: queryKeys.apiKeys })
    void qc.invalidateQueries({ queryKey: queryKeys.connect })
  }

  const create = useMutation({
    mutationFn: (name: string) => api.createApiKey(name),
    onSuccess: invalidate,
  })
  const remove = useMutation({
    mutationFn: (id: string) => api.deleteApiKey(id),
    onSuccess: invalidate,
  })
  const setEnabled = useMutation({
    mutationFn: (vars: { id: string; enabled: boolean }) =>
      api.setApiKeyEnabled(vars.id, vars.enabled),
    onSuccess: invalidate,
  })

  return { create, remove, setEnabled }
}
