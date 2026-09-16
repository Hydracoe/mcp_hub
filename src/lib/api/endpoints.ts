import { request } from './client'
import { adaptProbeResult } from './types'
import type {
  ApiKeyCreateResponse,
  ApiKeyView,
  Category,
  ConnectResponse,
  CredentialImportRequest,
  CredentialListResponse,
  CredentialRequest,
  CredentialView,
  KeyEventItem,
  LogItem,
  LogListResponse,
  LogQuery,
  OverviewResponse,
  ProbeDraftResult,
  ProbeResult,
  RawProbeResult,
  ServerDetail,
  ServerRequest,
  ServerSummary,
  TestAllResponse,
  ToolChangeListResponse,
  ToolChangeQuery,
  ToolEntry,
} from './types'

/** 管理 API 唯一调用入口，页面禁止裸 fetch：路径/参数散落会让后端改名时要全仓搜。 */

/* ── 概览 / 接入 ── */

export const getOverview = () => request<OverviewResponse>('/api/overview')

export const getConnect = () => request<ConnectResponse>('/api/connect')

/* ── Server ── */

export const listServers = () => request<{ total: number; items: ServerSummary[] }>('/api/servers')

export const getServer = (id: string) => request<ServerDetail>(`/api/servers/${id}`)

export const createServer = (body: ServerRequest, opts?: { probe?: boolean; upsert?: boolean }) =>
  request<ServerDetail>('/api/servers', {
    method: 'POST',
    query: { probe: opts?.probe ? 1 : undefined, upsert: opts?.upsert ? 1 : undefined },
    body,
  })

export const updateServer = (id: string, body: ServerRequest) =>
  request<ServerDetail>(`/api/servers/${id}`, { method: 'PUT', body })

/** 干跑探活：直接对请求体里的配置做 L1–L3 + tools/list，不落库。
 *  新增向导「探活 → 勾选启用」的入口，正式创建走 createServer。 */
export const probeServerDraft = (body: ServerRequest) =>
  request<ProbeDraftResult>('/api/servers/probe', { method: 'POST', body })

export const deleteServer = (id: string) =>
  request<void>(`/api/servers/${id}`, { method: 'DELETE' })

export const setServerEnabled = (id: string, enabled: boolean) =>
  request<ServerDetail>(`/api/servers/${id}/${enabled ? 'enable' : 'disable'}`, { method: 'POST' })

/** 单个 server 探活。后端直出原始 ProbeResult，这里折成 UI 的分层结构，
 *  ProbePanel 依赖的 levels/totalMs 由适配层保证存在。 */
export const testServer = async (id: string): Promise<ProbeResult> =>
  adaptProbeResult(await request<RawProbeResult>(`/api/servers/${id}/test`, { method: 'POST' }))

export const refreshServerTools = async (id: string): Promise<ProbeResult> =>
  adaptProbeResult(
    await request<RawProbeResult>(`/api/servers/${id}/refresh-tools`, { method: 'POST' }),
  )

export const testAllServers = () =>
  request<TestAllResponse>('/api/servers/test-all', { method: 'POST' })

/* ── 工具 ── */

export const listTools = (params?: { serverKey?: string; q?: string }) =>
  request<{ total: number; items: ToolEntry[] }>('/api/tools', {
    query: { serverKey: params?.serverKey, q: params?.q },
  })

/** 启用 / 禁用单个工具。响应回整份 server 详情，界面拨完开关就地刷新。
 *  工具名进路径，按 MCP 规范只含安全字符，仍统一编码以防万一。 */
export const setToolEnabled = (serverId: string, toolName: string, enabled: boolean) =>
  request<ServerDetail>(
    `/api/servers/${serverId}/tools/${encodeURIComponent(toolName)}/${enabled ? 'enable' : 'disable'}`,
    { method: 'POST' },
  )

/* ── 工具变更记录 ── */

export const listToolChanges = (query: ToolChangeQuery = {}) =>
  request<ToolChangeListResponse>('/api/tool-changes', {
    query: {
      serverId: query.serverId,
      limit: query.limit,
      offset: query.offset,
    },
  })

/* ── 分类 ── */

export const listCategories = () =>
  request<{ total: number; items: Category[] }>('/api/categories')

export interface CategoryRequest {
  /** 中文名，界面展示。 */
  name: string
  /** 英文名（slug），用于列表与筛选，不进对外工具名。 */
  slug: string
  description: string
}

export const createCategory = (body: CategoryRequest) =>
  request<Category>('/api/categories', { method: 'POST', body })

export const updateCategory = (id: string, body: CategoryRequest) =>
  request<Category>(`/api/categories/${id}`, { method: 'PUT', body })

export const deleteCategory = (id: string) =>
  request<void>(`/api/categories/${id}`, { method: 'DELETE' })

/* ── 日志 ── */

export const listLogs = (query: LogQuery) =>
  request<LogListResponse>('/api/logs', {
    query: {
      serverId: query.serverId,
      status: query.status,
      kind: query.kind,
      apiKeyId: query.apiKeyId,
      q: query.q,
      since: query.since,
      until: query.until,
      sort: query.sort,
      order: query.order,
      limit: query.limit,
      offset: query.offset,
    },
  })

export const clearLogs = () => request<{ deleted: number }>('/api/logs', { method: 'DELETE' })

/** 密钥事件流。后端暂无此端点，返回 404 时界面要降级而非崩溃。 */
export const listKeyEvents = (query: { serverId?: string; limit?: number } = {}) =>
  request<{ total: number; items: KeyEventItem[] }>('/api/logs/key-events', {
    query: { serverId: query.serverId, limit: query.limit ?? 100 },
  })

/* ── 密钥池 ── */

export const listCredentials = (serverId: string) =>
  request<CredentialListResponse>(`/api/servers/${serverId}/credentials`)

export const createCredential = (serverId: string, body: CredentialRequest) =>
  request<CredentialView>(`/api/servers/${serverId}/credentials`, { method: 'POST', body })

export const updateCredential = (serverId: string, cid: string, body: CredentialRequest) =>
  request<CredentialView>(`/api/servers/${serverId}/credentials/${cid}`, { method: 'PUT', body })

export const deleteCredential = (serverId: string, cid: string) =>
  request<void>(`/api/servers/${serverId}/credentials/${cid}`, { method: 'DELETE' })

export const setCredentialEnabled = (serverId: string, cid: string, enabled: boolean) =>
  request<CredentialView>(
    `/api/servers/${serverId}/credentials/${cid}/${enabled ? 'enable' : 'disable'}`,
    { method: 'POST' },
  )

export const testCredential = (serverId: string, cid: string) =>
  request<ProbeResult>(`/api/servers/${serverId}/credentials/${cid}/test`, { method: 'POST' })

export const resetCredentialQuota = (serverId: string, cid: string) =>
  request<CredentialView>(`/api/servers/${serverId}/credentials/${cid}/reset-quota`, {
    method: 'POST',
  })

export const importCredentials = (serverId: string, body: CredentialImportRequest) =>
  request<{ created: number; items: CredentialView[] }>(
    `/api/servers/${serverId}/credentials/import`,
    { method: 'POST', body },
  )

/* ── 接入 API Key（上游客户端调 /mcp 的凭据） ── */

export const listApiKeys = () => request<{ total: number; items: ApiKeyView[] }>('/api/keys')

/** 创建一把接入密钥。响应里的 `key` 是明文，只此一次。 */
export const createApiKey = (name: string) =>
  request<ApiKeyCreateResponse>('/api/keys', { method: 'POST', body: { name } })

/** 删除即失效：后续用这把 key 访问 /mcp 会立刻拿到 401。 */
export const deleteApiKey = (id: string) => request<void>(`/api/keys/${id}`, { method: 'DELETE' })

/** 启用 / 停用一把接入密钥（后端返回 204，无响应体）。
 *  停用是收紧不是放开：库里存在过 key 时 `/mcp` 强制鉴权，全部停用等于锁死端点，而非退回匿名访问。 */
export const setApiKeyEnabled = (id: string, enabled: boolean) =>
  request<void>(`/api/keys/${id}/${enabled ? 'enable' : 'disable'}`, { method: 'POST' })

export type { LogItem }
