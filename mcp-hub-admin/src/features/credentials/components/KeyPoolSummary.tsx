import { formatInt } from '@/lib/format'

export interface CredentialCounts {
  usable: number
  cooling: number
  disabled: number
  unavailable: number
}

/** 池子现状：可用 / 冷却 / 停用。 */
export function KeyPoolSummary({ counts }: { counts: CredentialCounts }) {
  return (
    <span className="flex items-center gap-x-3 gap-y-1 text-[11.5px] text-muted-foreground">
      <span>
        可用 <span className="tabular-nums text-success-strong">{formatInt(counts.usable)}</span>
      </span>
      <span>
        冷却 <span className="tabular-nums text-warning-strong">{formatInt(counts.cooling)}</span>
      </span>
      <span>
        停用 <span className="tabular-nums text-error-strong">{formatInt(counts.disabled)}</span>
      </span>
    </span>
  )
}

/** 页脚注明「不可用合计」的口径，避免误读 0。 */
export function CredentialsTableFoot({ counts }: { counts: CredentialCounts }) {
  return (
    <div className="flex flex-wrap items-center gap-x-4 gap-y-1 border-t border-separator px-4 py-2.5 text-[11.5px] text-muted-foreground">
      <span>
        配额一列是每分钟请求数（RPM）的窗口用量，到下一个整分钟自动清零。
      </span>
      <span className="tabular-nums">不可用合计 {formatInt(counts.unavailable)} 把</span>
    </div>
  )
}
