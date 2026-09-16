import {
  useMutation,
  useQuery,
  useQueryClient,
  type UseQueryOptions,
} from '@tanstack/react-query'

import * as api from './endpoints'
import type {
  Category,
  CredentialRequest,
  LogQuery,
  ServerRequest,
} from './types'

/**
 * 查询键。
 *
 * 集中在一处是为了 `invalidateQueries` 能用前缀批量失效 ——
 * 散布的字符串数组写错一个字母，失效就静默不发生，
 * 症状是「改完了但界面没变」。
 */
export const queryKeys = {
  overview: ['overview'] as const,
  connect: ['connect'] as const,
  servers: ['servers'] as const,
  server: (id: string) => ['servers', id] as const,
  credentials: (serverId: string) => ['servers', serverId, 'credentials'] as const,
  tools: (params?: { serverKey?: string; q?: string }) => ['tools', params ?? {}] as const,
  toolSearch: (q: string, mode: string) => ['tools', 'search', q, mode] as const,
  logs: (query: LogQuery) => ['logs', query] as const,
  keyEvents: (serverId?: string) => ['logs', 'key-events', serverId ?? ''] as const,
  categories: ['categories'] as const,
  apiKeys: ['keys'] as const,
  embedConfig: ['embed-config'] as const,
  embedRebuild: ['embed-config', 'rebuild'] as const,
}

/** 轮询节奏：概览 15s（设计稿标注），日志 5s（PRD §10.3 选了轮询而非 SSE）。 */
export const POLL = {
  overview: 15_000,
  servers: 20_000,
  logs: 5_000,
  credentials: 10_000,
} as const

/* ────────────────────────────── 查询 ────────────────────────────── */

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

export function useCredentials(serverId: string | undefined, enabled = true) {
  return useQuery({
    queryKey: queryKeys.credentials(serverId ?? ''),
    queryFn: () => api.listCredentials(serverId as string),
    enabled: Boolean(serverId) && enabled,
    refetchInterval: POLL.credentials,
  })
}

export function useTools(params?: { serverKey?: string; q?: string }) {
  return useQuery({
    queryKey: queryKeys.tools(params),
    queryFn: () => api.listTools(params),
  })
}

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
    // 缺口 B1：真实后端可能没有这个端点（404）。
    // 让它安静失败，由界面降级成「该视图暂未开放」，不要反复重试。
    retry: false,
  })
}

export function useCategories() {
  return useQuery({ queryKey: queryKeys.categories, queryFn: api.listCategories })
}

/**
 * 接入密钥清单。
 *
 * 刻意不轮询：这张表的变化只来自本页操作，而每个操作都会主动失效它。
 * 加轮询的唯一下场是用户正对着"刚创建、还没复制"的密钥时列表突然刷新。
 */
export function useApiKeys() {
  return useQuery({ queryKey: queryKeys.apiKeys, queryFn: api.listApiKeys })
}

/* ────────────────────────────── 变更 ────────────────────────────── */

/** 服务端数据被改动后，统一失效哪些查询。 */
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
      // 缺口 B4：后端不保存最近一次全量测试报告，前端内存留一份即可。
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

/* ── 密钥池 ── */

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

/* ── 日志 ── */

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

/* ── 分类 ── */

export function useCategoryMutations() {
  const qc = useQueryClient()
  const invalidate = () => {
    void qc.invalidateQueries({ queryKey: queryKeys.categories })
    void qc.invalidateQueries({ queryKey: queryKeys.servers })
    // 改英文名会改掉该分类下所有工具的对外名，工具市场与概览
    // 里的计数都会跟着变，不失效就会显示上一个名字。
    void qc.invalidateQueries({ queryKey: ['tools'] })
    void qc.invalidateQueries({ queryKey: queryKeys.overview })
  }
  const create = useMutation({
    mutationFn: (body: api.CategoryRequest) => api.createCategory(body),
    onSuccess: invalidate,
  })
  const update = useMutation({
    mutationFn: (vars: { id: string; body: api.CategoryRequest }) =>
      api.updateCategory(vars.id, vars.body),
    onSuccess: invalidate,
  })
  const remove = useMutation({
    mutationFn: (id: string) => api.deleteCategory(id),
    onSuccess: invalidate,
  })
  return { create, update, remove }
}

/* ── 接入密钥（上游客户端调 /mcp 的凭据） ── */

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

export type { Category }
