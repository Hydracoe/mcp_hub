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
  }
}

/** 从表单状态组装请求体。
 *  headers / env 是后端的「三态」字段：没带 = 沿用原值，显式空 = 清空，非空 = 整体替换。
 *  掩码 *** 必须原样回传（丢掉会变成空 map 即清空）；当前形态用不到的那组显式给空。 */
export function buildServerRequest(s: ServerFormState): ServerRequest {
  const req: ServerRequest = {
    key: s.key.trim(),
    name: s.name,
    transport: s.transport,
    description: s.description.trim() || undefined,
    tags: s.tags.length ? s.tags : undefined,
    categoryId: s.categoryId || undefined,
    enabled: s.enabled,
    timeoutMs: s.timeoutMs,
    protocolVersion: s.protocolVersion.trim() || undefined,
    legacyOnly: s.legacyOnly,
  }

  if (s.transport === 'stdio') {
    const args = splitLines(s.argsText)
    req.command = s.command.trim() || undefined
    req.args = args.length ? args : undefined
    req.env = s.env.filter((r) => r.key.trim()).map((r) => `${r.key.trim()}=${r.value}`)
    // stdio 不走请求头：从远程切过来时旧 headers 要显式清掉。
    req.headers = {}
    return req
  }

  req.url = s.url.trim() || undefined
  req.authMode = s.authMode
  // 远程不走环境变量：从 stdio 切过来时旧 env 要显式清掉。
  req.env = []
  if (s.authMode === 'header') {
    const headers: Record<string, string> = {}
    for (const r of s.headers) {
      const k = r.key.trim()
      if (!k) continue
      // 值仍是 *** 说明这一行未改：原样回传，后端按原值还原。
      headers[k] = r.value
    }
    req.headers = headers
  } else {
    // 改成「不需要鉴权」：显式清空 headers，否则旧凭据会继续被注入下游。
    req.headers = {}
  }
  return req
}

/** 分类下拉的「未分类」保留值。空 categoryId 在接口里表示"不设分类"。 */
export const NO_CATEGORY = '__none'

/** 工具前缀的格式规则，必须与后端 downstream 的 keyPattern 一致：
 *  字母/数字开头，其后只允许字母、数字与 -。 */
const SERVER_KEY_RE = /^[A-Za-z0-9][A-Za-z0-9-]*$/

/** Key 的本地校验。后端也会拒（400），但那是一条英文原文；
 *  这里先给出中文提示，也免得白跑一趟。 */
export function keyFormatError(key: string): string | undefined {
  const k = key.trim()
  if (!k) return '必填'
  if (!SERVER_KEY_RE.test(k)) return '仅允许字母、数字和 -，且必须以字母或数字开头'
  if (k.toLowerCase() === 'hub') return 'hub 是保留前缀（网关自带工具占用），请换一个'
  return undefined
}
