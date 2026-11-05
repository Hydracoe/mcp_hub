/** 展示层格式化。集中放这里，免得同一个数字在不同页面位数不一致。 */

/** 千分位整数：1284 → "1,284"。 */
export function formatInt(n: number | undefined | null): string {
  if (n === undefined || n === null || Number.isNaN(n)) return '—'
  return Math.round(n).toLocaleString('en-US')
}

/** 比率（0–1）→ 百分比。默认 2 位；传空返回占位符，不显示 NaN%。 */
export function formatPercent(ratio: number | undefined | null, digits = 2): string {
  if (ratio === undefined || ratio === null || Number.isNaN(ratio)) return '—'
  return `${(ratio * 100).toFixed(digits)}%`
}

/** 毫秒 → "45ms" / "1.24s" / "1min 12s"。 */
export function formatDuration(ms: number | undefined | null): string {
  if (ms === undefined || ms === null || Number.isNaN(ms)) return '—'
  if (ms < 0) return '—'
  if (ms < 1000) return `${Math.round(ms)}ms`
  if (ms < 60_000) return `${(ms / 1000).toFixed(2)}s`
  const totalSec = Math.round(ms / 1000)
  const m = Math.floor(totalSec / 60)
  const s = totalSec % 60
  return `${m}min ${s}s`
}

/** 剩余时长倒计时：834000 → "13min 54s"。 */
export function formatCountdown(ms: number | undefined | null): string {
  if (ms === undefined || ms === null || ms <= 0) return '已到期'
  const totalSec = Math.ceil(ms / 1000)
  const h = Math.floor(totalSec / 3600)
  const m = Math.floor((totalSec % 3600) / 60)
  const s = totalSec % 60
  if (h > 0) return `${h}h ${m}min`
  if (m > 0) return `${m}min ${String(s).padStart(2, '0')}s`
  return `${s}s`
}

const pad = (n: number) => String(n).padStart(2, '0')

/** Unix 毫秒 → "2026-09-15 17:40:56"。 */
export function formatDateTime(ts: number | string | undefined | null): string {
  const d = toDate(ts)
  if (!d) return '—'
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`
}

/** Unix 毫秒 → "2026-09-15 17:40:56.412"。 */
export function formatDateTimeMs(ts: number | string | undefined | null): string {
  const d = toDate(ts)
  if (!d) return '—'
  return `${formatDateTime(d.getTime())}.${String(d.getMilliseconds()).padStart(3, '0')}`
}

/** 相对时间：3m ago / 2h ago / 3d ago。 */
export function formatRelative(ts: number | string | undefined | null, now = Date.now()): string {
  const d = toDate(ts)
  if (!d) return '—'
  const diff = now - d.getTime()
  if (diff < 0) return '刚刚'
  const sec = Math.floor(diff / 1000)
  if (sec < 60) return `${sec}s ago`
  const min = Math.floor(sec / 60)
  if (min < 60) return `${min}m ago`
  const hour = Math.floor(min / 60)
  if (hour < 24) return `${hour}h ago`
  return `${Math.floor(hour / 24)}d ago`
}

/** 运行时长：秒 → "3d 4h" / "12min"。 */
export function formatUptime(seconds: number | undefined | null): string {
  if (seconds === undefined || seconds === null) return '—'
  const d = Math.floor(seconds / 86400)
  const h = Math.floor((seconds % 86400) / 3600)
  const m = Math.floor((seconds % 3600) / 60)
  if (d > 0) return `${d}d ${h}h`
  if (h > 0) return `${h}h ${m}min`
  return `${m}min`
}

export function transportLabel(t: string): string {
  return t === 'streamable_http' ? 'streamable_http' : t
}

function toDate(ts: number | string | undefined | null): Date | null {
  if (ts === undefined || ts === null || ts === '') return null
  // 后端同时接受 Unix 毫秒与 RFC3339，两种都认。
  const d = typeof ts === 'number' ? new Date(ts) : new Date(/^\d+$/.test(ts) ? Number(ts) : ts)
  return Number.isNaN(d.getTime()) ? null : d
}

/** 从 URL / 命令行里取出可读的短目标名。 */
export function shortTarget(target: string): string {
  try {
    if (/^https?:\/\//.test(target)) return new URL(target).host
  } catch {
    /* 不是合法 URL，按命令处理 */
  }
  return target
}

/** 宽松的 JSON 格式化：解析失败时原样返回，不抛。 */
export function safePrettyJson(raw: string | undefined | null): string {
  if (!raw) return ''
  try {
    return JSON.stringify(JSON.parse(raw), null, 2)
  } catch {
    return raw
  }
}
