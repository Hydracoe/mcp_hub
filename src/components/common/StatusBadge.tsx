import { Badge } from '@/components/ui/badge'
import type { CredentialState, DisabledKind, ServerStatus } from '@/lib/api/types'

/**
 * 状态徽标。
 *
 * 语义色只在这一个文件里与文案挂钩。设计稿把
 * `background:color-mix(in srgb, var(--state-success) 12%, transparent)`
 * 抄到了 30 多处、透明度还各不相同（12% / 15%），改一次配色要翻遍全站。
 */

const SERVER_STATUS: Record<ServerStatus, { label: string; variant: 'success' | 'warning' | 'error' | 'neutral' }> = {
  healthy: { label: 'healthy', variant: 'success' },
  degraded: { label: 'degraded', variant: 'warning' },
  offline: { label: 'offline', variant: 'error' },
  unknown: { label: 'unknown', variant: 'neutral' },
}

export function ServerStatusBadge({ status }: { status: ServerStatus }) {
  const cfg = SERVER_STATUS[status] ?? SERVER_STATUS.unknown
  return <Badge variant={cfg.variant}>{cfg.label}</Badge>
}

/**
 * 「停用中」与 `status` 是两回事：status 说的是下游健不健康，
 * draining 说的是网关打算怎么处置它。混在一起会让用户以为
 * 点了停用就当场断干净了，而实际上还有调用在收尾。
 */
export function DrainingBadge({ inflight }: { inflight?: number }) {
  return (
    <Badge variant="neutral">
      停用中
      {inflight ? <span className="tabular-nums">· {inflight} 个收尾</span> : null}
    </Badge>
  )
}

export function StoppedBadge() {
  return <Badge variant="neutral">已停止</Badge>
}

const CRED_STATE: Record<CredentialState, { label: string; variant: 'success' | 'warning' | 'error' }> = {
  active: { label: '可用', variant: 'success' },
  cooling: { label: '冷却中', variant: 'warning' },
  disabled: { label: '已弃用', variant: 'error' },
}

export function CredentialStateBadge({
  state,
  disabledKind,
  enabled = true,
}: {
  state: CredentialState
  disabledKind?: DisabledKind
  enabled?: boolean
}) {
  if (!enabled && state !== 'disabled') return <Badge variant="neutral">已停用</Badge>
  const cfg = CRED_STATE[state] ?? CRED_STATE.active
  // 弃用时把原因直接写进徽标：用户要的是「为什么这把不能用了」，
  // 而不是「它不能用了」。
  return <Badge variant={cfg.variant}>{state === 'disabled' && disabledKind ? disabledKind : cfg.label}</Badge>
}

const ERROR_KIND_VARIANT: Record<string, 'warning' | 'error' | 'neutral'> = {
  RATE_LIMITED: 'warning',
  QUOTA_EXHAUSTED: 'error',
  TIMEOUT: 'warning',
  CONN_REFUSED: 'error',
  DNS_FAIL: 'error',
  TLS_FAIL: 'error',
  AUTH_REQUIRED: 'error',
  NOT_FOUND: 'error',
  UPSTREAM_ERROR: 'error',
  NOT_MCP: 'error',
  PROTOCOL_MISMATCH: 'error',
  CAPABILITY_MISSING: 'warning',
  SPAWN_FAILED: 'error',
  EXIT_EARLY: 'error',
  HANDSHAKE_TIMEOUT: 'error',
  SERVER_DRAINING: 'neutral',
  CANCELED: 'neutral',
  NO_USABLE_KEY: 'error',
}

/** 错误分类徽标。等宽字体，便于在日志表里纵向对齐。 */
export function ErrorKindBadge({ kind, className }: { kind?: string; className?: string }) {
  if (!kind) return <span className="text-muted-foreground">—</span>
  return (
    <Badge variant={ERROR_KIND_VARIANT[kind] ?? 'neutral'} className={className}>
      <span className="font-mono">{kind}</span>
    </Badge>
  )
}

/** 密钥事件类型徽标（KEY_COOLDOWN / KEY_DISABLED / KEY_RECOVERED）。 */
export function KeyEventBadge({ type }: { type: 'KEY_COOLDOWN' | 'KEY_DISABLED' | 'KEY_RECOVERED' }) {
  const map = {
    KEY_COOLDOWN: 'warning',
    KEY_DISABLED: 'error',
    KEY_RECOVERED: 'success',
  } as const
  const dot = {
    KEY_COOLDOWN: 'var(--state-warning)',
    KEY_DISABLED: 'var(--state-error)',
    KEY_RECOVERED: 'var(--state-success)',
  } as const
  return (
    <Badge variant={map[type]} className="rounded-full! gap-1.5!">
      <span className="size-1.5 rounded-full" style={{ background: dot[type] }} />
      <span className="font-mono">{type}</span>
    </Badge>
  )
}

/** 传输类型标签。等宽小片，列表页用。 */
export function TransportTag({ transport }: { transport: string }) {
  return (
    <span className="inline-block rounded bg-muted px-1.5 py-0.5 font-mono text-[11px] text-muted-foreground">
      {transport}
    </span>
  )
}
