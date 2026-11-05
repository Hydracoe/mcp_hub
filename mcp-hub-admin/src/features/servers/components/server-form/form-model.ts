import type { AuthMode, ServerRequest, Transport } from '@/lib/api/types'

/** 新增 Server 向导的纯数据模型（不依赖 React）：表单形状、初始值与请求体转换。 */

export interface ProbeTool {
  name: string
  description?: string
}

export interface ProbeFailure {
  kind?: string
  hint?: string
  detail?: string
  stderr?: string
}

export interface HeaderRow {
  k: string
  v: string
}

export interface ServerFormState {
  /* ── 基础与传输 ── */
  name: string
  key: string
  enabled: boolean
  transport: Transport
  url: string
  command: string
  argsText: string
  envText: string
  protocolVersion: string
  legacyOnly: boolean
  timeoutMs: number

  /* ── 鉴权与元信息 ── */
  needsKey: boolean
  headerRows: HeaderRow[]
  description: string
  tagsText: string
  categoryId: string

  /** 提交创建时是否顺带 ?probe=1 刷新工具缓存。 */
  probe: boolean

  /* ── 探活与启用（Step 3） ── */
  probeStatus: 'idle' | 'probing' | 'ok' | 'fail'
  probeTools: ProbeTool[]
  /** 工具名 → 是否启用。默认全选。 */
  probeSelected: Record<string, boolean>
  probeFail: ProbeFailure | null
  /** 探活时的配置签名；连接字段变了就要求重新探活。 */
  probeSig: string
  /** 探活失败时的兜底：不阻塞创建，工具之后在详情页启用。 */
  fallbackAll: boolean
}

export const EMPTY_SERVER_FORM: ServerFormState = {
  name: '',
  key: '',
  enabled: true,
  transport: 'streamable_http',
  url: '',
  command: '',
  argsText: '',
  envText: '',
  protocolVersion: '',
  legacyOnly: false,
  timeoutMs: 30000,
  needsKey: true,
  headerRows: [{ k: '', v: '' }],
  description: '',
  tagsText: '',
  categoryId: '',
  probe: true,
  probeStatus: 'idle',
  probeTools: [],
  probeSelected: {},
  probeFail: null,
  probeSig: '',
  fallbackAll: false,
}

export const WIZARD_STEPS = ['基础与传输', '鉴权与元信息', '探活与启用'] as const
export type WizardStep = 1 | 2 | 3

/** 探活结果回到「未探活」。改连接配置后必须调用。 */
const CLEARED_PROBE = {
  probeStatus: 'idle',
  probeTools: [],
  probeSelected: {},
  probeFail: null,
  probeSig: '',
  fallbackAll: false,
} satisfies Partial<ServerFormState>

export const clearedProbe = (): Partial<ServerFormState> => ({ ...CLEARED_PROBE })

const splitLines = (text: string) =>
  text
    .split('\n')
    .map((s) => s.trim())
    .filter(Boolean)

const splitCsv = (text: string) =>
  text
    .split(',')
    .map((s) => s.trim())
    .filter(Boolean)

/** 表头行 → 对象。名称为空的行丢弃。 */
const toHeaders = (rows: HeaderRow[]): Record<string, string> =>
  Object.fromEntries(rows.map((r) => [r.k.trim(), r.v]).filter(([k]) => k))

/** 影响探活结论的字段签名；新增连接字段时必须同步加进来。 */
export const connectionSignature = (f: ServerFormState): string =>
  JSON.stringify([
    f.key.trim(),
    f.transport,
    f.url.trim(),
    f.command.trim(),
    f.argsText,
    f.envText,
    f.protocolVersion.trim(),
    f.legacyOnly,
    f.timeoutMs,
    f.needsKey,
    f.headerRows,
  ])

/** 探活请求体：只带连接字段，启用名单尚未确定。 */
export const toProbeRequest = (f: ServerFormState): ServerRequest => {
  const body: ServerRequest = {
    key: f.key.trim(),
    name: f.name.trim(),
    transport: f.transport,
    timeoutMs: Number(f.timeoutMs) || undefined,
  }

  if (f.transport === 'stdio') {
    body.command = f.command.trim()
    const args = splitLines(f.argsText)
    if (args.length) body.args = args
    const env = splitLines(f.envText)
    if (env.length) body.env = env
    return body
  }

  body.url = f.url.trim()
  if (f.protocolVersion.trim()) body.protocolVersion = f.protocolVersion.trim()
  if (f.transport === 'streamable_http') body.legacyOnly = f.legacyOnly
  const authMode: AuthMode = f.needsKey ? 'header' : 'none'
  body.authMode = authMode
  if (f.needsKey) {
    const headers = toHeaders(f.headerRows)
    if (Object.keys(headers).length) body.headers = headers
  }
  return body
}

/** 创建请求体：探活请求 + 初始启用名单 + 元信息。 */
export const toCreateRequest = (f: ServerFormState): ServerRequest => {
  const body = toProbeRequest(f)

  const selected = f.probeTools.filter((t) => f.probeSelected[t.name]).map((t) => t.name)
  // 勾选结果就是「首次探活落库时启用哪些」的引导名单：新工具默认禁用，
  // 不带它的话探活到的工具会全部停在禁用态，向导勾选等于白勾。
  if (f.probeStatus === 'ok' && selected.length > 0) {
    body.enabledTools = selected
  }

  if (f.description.trim()) body.description = f.description.trim()
  if (f.tagsText.trim()) body.tags = splitCsv(f.tagsText)
  body.categoryId = f.categoryId
  return body
}

/** 已选工具数（「已选 N/M」与提交前的非空校验共用）。 */
export const countSelectedTools = (f: ServerFormState): number =>
  f.probeTools.filter((t) => f.probeSelected[t.name]).length

/** 当前勾选是否全选（步骤提示「已选 N/M」共用）。 */
export const isAllToolsSelected = (f: ServerFormState): boolean =>
  f.probeTools.length > 0 && countSelectedTools(f) === f.probeTools.length

/** 探活成功后连接配置又被改过：旧清单已作废。 */
export const isProbeStale = (f: ServerFormState): boolean =>
  f.probeSig !== '' && f.probeSig !== connectionSignature(f)
