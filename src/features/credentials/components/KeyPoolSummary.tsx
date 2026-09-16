import { formatInt } from '@/lib/format'

export interface CredentialCounts {
  usable: number
  cooling: number
  disabled: number
  unavailable: number
  hasUnmetered: boolean
}

/** 池子现状：可用 / 冷却 / 停用三个数。和两个入口按钮同一行。 */
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

/** 表格页脚注释。把「未计量」与「不可用合计」的口径写在这里，避免用户误读 0。 */
export function CredentialsTableFoot({ counts }: { counts: CredentialCounts }) {
  return (
    <div className="flex flex-wrap items-center gap-x-4 gap-y-1 border-t border-separator px-4 py-2.5 text-[11.5px] text-muted-foreground">
      <span>
        TPM 未计量的密钥显示「未计量」而不是 0 —— 下游没回 usage 时，0 会被误读成「没用过」。
      </span>
      <span className="tabular-nums">
        不可用合计 {formatInt(counts.unavailable)} 把
        {counts.hasUnmetered ? ' · 含未计量项' : ''}
      </span>
    </div>
  )
}
