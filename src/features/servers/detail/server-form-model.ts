import type { AuthMode, ServerDetail, ServerRequest, Transport } from '@/lib/api/types'

import type { KeyValueRow } from './components/FormBits'

/** 详情页编辑表单的纯数据模型。init ↔ request ↔ 脏检查三者必须放在一起，
 *  否则表单形状与提交内容分家会让脏检查误报或漏报。 */

export interface ServerFormState {
  key: string
  name: string
  transport: Transport
  description: string
  tags: string[]
  categoryId: string
  enabled: boolean
  // stdio
  command: string
  argsText: string
  env: KeyValueRow[]
  // 远程
  url: string
  headers: KeyValueRow[]
  authMode: AuthMode
  authHeaderName: string
  // 高级
  timeoutMs: number
  protocolVersion: string
  legacyOnly: boolean
  estTokensPerCall: number
}

const splitLines = (text: string) =>
  text
    .split('\n')
    .map((s) => s.trim())
    .filter(Boolean)

const parseEnv = (env: string[]): KeyValueRow[] =>
  env.map((e) => {
    const i = e.indexOf('=')
    return i < 0 ? { key: e, value: '' } : { key: e.slice(0, i), value: e.slice(i + 1) }
  })

const parseHeaders = (c: ServerDetail['config']): KeyValueRow[] => {
  if (c.headers && Object.keys(c.headers).length) {
    return Object.entries(c.headers).map(([key, value]) => ({ key, value }))
  }
  if (c.headerKeys) return c.headerKeys.map((key) => ({ key, value: '***' }))
  return []
}

export function createInitialForm(server: ServerDetail): ServerFormState {
  const c = server.config
  const headers = parseHeaders(c)
  return {
    key: server.key,
    name: server.name,
    transport: server.transport,
    description: server.description ?? '',
    tags: server.tags ?? [],
    categoryId: server.categoryId ?? '',
    enabled: server.enabled,
    command: c.command ?? '',
    argsText: (c.args ?? []).join('\n'),
    env: parseEnv(c.env ?? []),
    url: c.url ?? '',
    headers,
    authMode: c.authMode ?? server.authMode ?? 'none',
    authHeaderName: headers[0]?.key ?? 'Authorization',
    timeoutMs: c.timeoutMs,
    protocolVersion: c.protocolVersion ?? '',
    legacyOnly: c.legacyOnly,
    estTokensPerCall: c.estTokensPerCall ?? 0,
  }
}

/** 从表单状态组装请求体。`***` 一律不回传。 */
export function buildServerRequest(s: ServerFormState): ServerRequest {
  const req: ServerRequest = {
    key: s.key,
    name: s.name,
    transport: s.transport,
    description: s.description.trim() || undefined,
    tags: s.tags.length ? s.tags : undefined,
    categoryId: s.categoryId || undefined,
    enabled: s.enabled,
    timeoutMs: s.timeoutMs,
    protocolVersion: s.protocolVersion.trim() || undefined,
    legacyOnly: s.legacyOnly,
    estTokensPerCall: s.estTokensPerCall > 0 ? s.estTokensPerCall : undefined,
  }

  if (s.transport === 'stdio') {
    const args = splitLines(s.argsText)
    req.command = s.command.trim() || undefined
    req.args = args.length ? args : undefined
    req.env = s.env.filter((r) => r.key.trim()).map((r) => `${r.key.trim()}=${r.value}`)
    return req
  }

  req.url = s.url.trim() || undefined
  req.authMode = s.authMode
  if (s.authMode === 'header') {
    const headers: Record<string, string> = {}
    for (const r of s.headers) {
      // 回显值仍是 *** 说明未改，沿用原值：不能把 *** 当真值提交回去
      if (r.key.trim() && r.value !== '***') headers[r.key.trim()] = r.value
    }
    req.headers = headers
  }
  return req
}

/** 分类下拉的「未分类」保留值。空 categoryId 在接口里表示"不设分类"。 */
export const NO_CATEGORY = '__none'
