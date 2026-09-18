import type { AuthMode, ErrorKind, ServerStatus, Transport } from './common'
import type { KeySummary } from './overview'

/* ────────────────────────────── Server ────────────────────────────── */

export interface ServerSummary {
  id: string
  key: string
  name: string
  transport: Transport
  /** URL 或命令行。 */
  target: string

  enabled: boolean
  status: ServerStatus
  authMode: AuthMode

  protocolEra?: 'modern' | 'legacy'
  protocolVersion?: string
  serverName?: string
  serverVersion?: string
  capabilities?: string[]

  description?: string
  tags?: string[]
  categoryId?: string
  category?: string

  toolCount: number
  /** 人工启用的工具数。 */
  enabledToolCount: number
  /** 此刻真正进入聚合池的数量（已启用且 server 已启用）。
   *  server 停用时小于 enabledToolCount。 */
  exposedToolCount: number

  /** false 表示走单密钥模式（沿用 headers）。 */
  keyPool: boolean
  keyCount: number
  usableKeyCount: number
  coolingKeyCount?: number
  disabledKeyCount?: number
  keySuccessRate?: number

  /** 已从聚合列表撤下，但还有调用在收尾。与 status 是两回事。 */
  draining: boolean
  drainingInflight?: number
  drainingSince?: number

  toolSuccessRate?: number

  lastCheckAt?: number
  lastLatencyMs?: number
  lastErrorKind?: ErrorKind
  lastError?: string
  lastErrorHint?: string
  createdAt: number
  updatedAt: number
}

/** 可编辑的连接配置。密钥字段一律以 `***` 回显。 */
export interface ServerConfigView {
  url?: string
  headers?: Record<string, string>
  /** 配了哪些头（键名）。 */
  headerKeys?: string[]
  authMode?: AuthMode
  command?: string
  args?: string[]
  env?: string[]
  timeoutMs: number
  protocolVersion?: string
  legacyOnly: boolean
}

/** 详情页工具清单中的一个工具（带人工开关状态）。 */
export interface ServerToolItem {
  name: string
  exposedName: string
  description?: string
  inputSchema?: Record<string, unknown>
  /** 工具级人工开关；禁用后不进聚合端点、不可被检索。 */
  enabled: boolean
  /** 此刻是否真正进入聚合池（enabled 且 server 已启用）。 */
  exposed: boolean
  available: boolean
}

export interface ServerDetail extends ServerSummary {
  config: ServerConfigView
  /** 下游原始工具名。 */
  tools?: string[]
  /** 带开关状态的完整工具清单。 */
  toolItems?: ServerToolItem[]
  /** 探活结果随写入返回，不必再打一次 /test。 */
  probe?: ProbeResult
  /** 该 server 的密钥池汇总。 */
  credentials?: KeySummary
}

export interface ServerRequest {
  key: string
  name: string
  transport: Transport
  url?: string
  headers?: Record<string, string>
  authMode?: AuthMode
  command?: string
  args?: string[]
  env?: string[]
  timeoutMs?: number
  protocolVersion?: string
  legacyOnly?: boolean
  /** 创建时的初始启用名单（向导勾选）。仅首次创建生效，编辑请求忽略。 */
  enabledTools?: string[]
  description?: string
  tags?: string[]
  categoryId?: string
  enabled?: boolean
}

/** 干跑探活（配置尚未落库）。连接结论之外还带下游工具清单，供向导勾选启用；
 *  失败时 ok=false，错误分类与排查提示在 kind / hint / detail 里。 */
export interface ProbeDraftResult {
  ok: boolean
  serverName?: string
  serverVersion?: string
  protocolVersion?: string
  legacy?: boolean
  capabilities?: string[]
  toolCount: number
  /** 下游原始工具名与描述，供勾选启用。 */
  tools?: { name: string; description?: string }[]
  /** 工具在聚合端点上的对外名（带 key 前缀），与 tools 一一对应。 */
  exposedTools?: string[]
  stage?: string
  kind?: string
  detail?: string
  stderr?: string
  /** 中文排查方向。 */
  hint?: string
}

/* ─────────────────────── 探活 / 试调 ─────────────────────── */

export interface ProbeLevel {
  level: 'L1' | 'L2' | 'L3' | 'L4'
  label: string
  ok: boolean
  durationMs: number
  errorKind?: ErrorKind
  detail?: string
  /** stdio 下游失败时的 stderr 摘要——定位 stdio 问题唯一的线索。 */
  stderr?: string
}

export interface ProbeResult {
  ok: boolean
  protocolVersion?: string
  protocolEra?: 'modern' | 'legacy'
  serverName?: string
  serverVersion?: string
  capabilities?: string[]
  toolCount?: number
  levels: ProbeLevel[]
  totalMs: number
  errorKind?: ErrorKind
  error?: string
  /** 中文排查方向。 */
  hint?: string
}

/** 后端探活端点直出的原始结果（downstream.ProbeResult + hint/exposedTools）。
 *  形状与 UI 的 ProbeResult 不同：没有 levels/totalMs，只有分段耗时 latency
 *  与"停在哪一步"的 stage。 */
export interface RawProbeResult {
  ok: boolean
  protocolVersion?: string
  serverName?: string
  serverVersion?: string
  capabilities?: string[]
  /** 协商到旧版协议。 */
  legacy?: boolean
  toolCount?: number
  latency?: {
    networkMs?: number
    connectMs?: number
    discoverMs?: number
    listToolsMs?: number
    totalMs?: number
  }
  /** 探活停在哪一步：network / connect / discover / list_tools / call。 */
  stage?: string
  kind?: string
  detail?: string
  /** 仅 stdio：进程失败时的 stderr 摘要。 */
  stderr?: string
  hint?: string
  checkedAt?: string
  statusCode?: number
  exposedTools?: string[]
}

/** 后端失败阶段 → UI 层级：L1 连接（network+connect）、L2 协议握手（discover）、
 *  L3 工具发现（list_tools）。call 不会出现在探活里，归入 L3。 */
const STAGE_LEVEL: Record<string, 0 | 1 | 2> = {
  network: 0,
  connect: 0,
  discover: 1,
  list_tools: 2,
  call: 2,
}

/** 把后端原始探活结果折成 UI 的 ProbeResult。探测未到达的层级标 ok=false
 *  并注明「未执行」，失败层级携带错误分类与诊断信息。 */
export function adaptProbeResult(raw: RawProbeResult): ProbeResult {
  const lat = raw.latency ?? {}
  const failAt: number = raw.ok ? Infinity : (STAGE_LEVEL[raw.stage ?? ''] ?? Infinity)

  const levels: ProbeLevel[] = [
    {
      level: 'L1',
      label: '连接',
      ok: failAt > 0,
      durationMs: (lat.networkMs ?? 0) + (lat.connectMs ?? 0),
      ...(failAt === 0
        ? { errorKind: raw.kind as ErrorKind | undefined, detail: raw.detail, stderr: raw.stderr }
        : {}),
    },
    {
      level: 'L2',
      label: '协议握手',
      ok: failAt > 1,
      durationMs: lat.discoverMs ?? 0,
      ...(failAt === 1
        ? { errorKind: raw.kind as ErrorKind | undefined, detail: raw.detail }
        : failAt < 1
          ? { detail: '未执行' }
          : {}),
    },
    {
      level: 'L3',
      label: '工具发现',
      ok: failAt > 2,
      durationMs: lat.listToolsMs ?? 0,
      ...(failAt === 2
        ? { errorKind: raw.kind as ErrorKind | undefined, detail: raw.detail }
        : failAt < 2
          ? { detail: '未执行' }
          : {}),
    },
  ]

  const stageSum =
    (lat.networkMs ?? 0) + (lat.connectMs ?? 0) + (lat.discoverMs ?? 0) + (lat.listToolsMs ?? 0)

  return {
    ok: raw.ok,
    protocolVersion: raw.protocolVersion,
    protocolEra: raw.legacy ? 'legacy' : 'modern',
    serverName: raw.serverName,
    serverVersion: raw.serverVersion,
    capabilities: raw.capabilities,
    toolCount: raw.toolCount,
    levels,
    totalMs: lat.totalMs ?? stageSum,
    errorKind: raw.kind as ErrorKind | undefined,
    error: raw.detail,
    hint: raw.hint,
  }
}

/** 全量测试汇总。healthy + failed 恒等于 total，停用项单列。 */
export interface TestAllResponse {
  total: number
  healthy: number
  failed: number
  /** 用户主动停用的数量，不计入成败。 */
  disabled: number
  tookMs: number
  results: ProbeResultItem[]
  testedAt: number
}

export interface ProbeResultItem {
  serverId: string
  serverKey: string
  name: string
  ok: boolean
  enabled: boolean
  status: ServerStatus
  latencyMs?: number
  protocolVersion?: string
  toolCount?: number
  errorKind?: ErrorKind
  error?: string
  hint?: string
}
