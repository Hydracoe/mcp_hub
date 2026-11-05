import type { CredentialState, DisabledKind } from './common'

/* ────────────────────────────── 概览 ────────────────────────────── */

export interface ServerStats {
  total: number
  enabled: number
  /** 人工停用的 server 数。不参与健康分桶：停用后不再探活，
   *  status 是停用前的冻结值，数进 healthy/offline 都会失真。 */
  disabled: number
  healthy: number
  degraded: number
  offline: number
  unknown: number
  /** 启用中但没有任何启用工具的 server 数（停用期间工具被级联禁用，
   *  重新启用不自动恢复）——需要人工逐个放行工具或停用 server。 */
  enabledNoTools: number
}

export interface ToolStats {
  /** 聚合快照里的工具数，即此刻真的暴露给 AI 的那一批。 */
  total: number
  /** 下游声明的工具总数（工具缓存）。与 total 不等即"有工具没暴露"。 */
  cached?: number
  /** 缓存里有、但没进聚合列表的：人工禁用或 server 停用。
   *  后端直接给（cached - total），前端不用自己减。 */
  hidden?: number
  /** 探活新发现、默认禁用、且从未被人工处理过的工具数。
   *  每一个都在等一个决定（启用或拒绝）——「待人工处理」告警的数据源。 */
  pendingEnable?: number
}

export interface CallStats {
  total: number
  failed: number
  avgMs: number
  maxMs: number
  /** 后端已算好（0–1），前端不要重算。 */
  failureRate: number
}

/** 密钥池汇总。三种「用不上」分开计数：冷却会自己好、弃用需换密钥、人工停用需人改主意。 */
export interface KeySummary {
  total: number
  /** 配了密钥池的 server 数。未配的走单密钥模式，不在统计内。 */
  servers: number
  usable: number
  cooling: number
  disabled: number
  /** cooling + disabled，后端直接给。 */
  unusable: number
}

export interface OverviewResponse {
  ready: boolean
  builtAt: number
  uptimeSeconds: number
  /** 版本号，可能为空串，界面要兜底。 */
  version?: string
  servers: ServerStats
  tools: ToolStats
  calls: CallStats
  credentials: KeySummary
  endpoints: Record<string, string>
  /** 每把不可用密钥的运行时明细（冷却剩余 / 弃用原因）。 */
  keyDetails?: KeyDetail[]
}

export interface KeyDetail {
  serverId: string
  serverKey: string
  credentialId: string
  label: string
  state: CredentialState
  disabledKind?: DisabledKind
  disabledNote?: string
  /** 冷却剩余毫秒，state === 'cooling' 时有意义。 */
  coolingRemainingMs?: number
  lastError?: string
  lastUsedAt?: number
}
