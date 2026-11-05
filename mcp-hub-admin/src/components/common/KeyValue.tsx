import type { ReactNode } from 'react'

import { cn } from '@/lib/utils'

export interface KeyValueRowProps {
  label: string
  children: ReactNode
  /** 值用等宽字体渲染。默认 true —— 这里的值几乎都是标识符或数字。 */
  mono?: boolean
  className?: string
}

/** 一行「小写标签 + 值」(`.fingerprint-row`)。标签一律小号大写加字距，
 *  用来区分字段名与字段值。 */
export function KeyValueRow({ label, children, mono = true, className }: KeyValueRowProps) {
  return (
    <div className={cn('fingerprint-row', className)}>
      <span className="shrink-0 text-[10.5px] font-semibold tracking-[0.06em] text-icon-muted uppercase">
        {label}
      </span>
      <span className={cn('min-w-0 text-right break-all text-foreground', mono && 'font-mono tabular-nums')}>
        {children}
      </span>
    </div>
  )
}

export interface KeyValueGridProps {
  items: { key: string; label: string; value: ReactNode; tone?: 'default' | 'success' | 'warning' | 'error' }[]
  columns?: 2 | 3 | 4
  className?: string
}

/** 网格版键值区。详情页的运行时指标用它。 */
export function KeyValueGrid({ items, columns = 4, className }: KeyValueGridProps) {
  const toneClass = {
    default: 'text-foreground',
    success: 'text-success-strong',
    warning: 'text-warning-strong',
    error: 'text-error-strong',
  } as const

  return (
    <div
      className={cn('grid gap-x-4 gap-y-2.5', className)}
      style={{ gridTemplateColumns: `repeat(${columns}, minmax(0, 1fr))` }}
    >
      {items.map((item) => (
        <div key={item.key} className="min-w-0">
          <div className="mb-0.5 text-[10.5px] font-semibold tracking-[0.06em] text-icon-muted uppercase">
            {item.label}
          </div>
          <div className={cn('font-mono text-[12px] tabular-nums', toneClass[item.tone ?? 'default'])}>
            {item.value}
          </div>
        </div>
      ))}
    </div>
  )
}

export interface MetricCardProps {
  label: string
  icon?: ReactNode
  value: ReactNode
  /** 值的后缀说明，如「健康」「/ 3,186」。 */
  suffix?: ReactNode
  /** 底部的一行明细。 */
  detail?: ReactNode
  footnote?: ReactNode
  className?: string
}

/** 概览页指标卡，结构固定为「标签 + 大数字 + 明细 + 脚注」四段。 */
export function MetricCard({
  label,
  icon,
  value,
  suffix,
  detail,
  footnote,
  className,
}: MetricCardProps) {
  return (
    <article className={cn('ui-card p-5', className)}>
      <div className="flex items-center justify-between">
        <span className="text-[10.5px] font-semibold tracking-[0.065em] text-icon-muted uppercase">
          {label}
        </span>
        {icon}
      </div>
      <div className="mt-3 text-[22px] leading-none font-semibold text-foreground tabular-nums">
        {value}
        {suffix ? <span className="ml-1.5 text-[13px] font-medium text-muted-foreground">{suffix}</span> : null}
      </div>
      {detail ? <div className="mt-3.5 flex flex-wrap items-center gap-x-3 gap-y-1.5 text-[11.5px]">{detail}</div> : null}
      {footnote ? <p className="mt-3 text-[11.5px] text-muted-foreground">{footnote}</p> : null}
    </article>
  )
}

/** 指标卡明细里的一枚「圆点 + 文案」。 */
export function DotStat({
  tone,
  label,
  value,
}: {
  tone: 'success' | 'warning' | 'error' | 'neutral'
  label: string
  value?: ReactNode
}) {
  const color = {
    success: 'var(--state-success)',
    warning: 'var(--state-warning)',
    error: 'var(--state-error)',
    neutral: 'var(--icon-muted)',
  }[tone]
  return (
    <span className="inline-flex items-center gap-1.5 text-foreground">
      <span className="size-2 shrink-0 rounded-full" style={{ background: color }} />
      {label}
      {value !== undefined ? <span className="tabular-nums">{value}</span> : null}
    </span>
  )
}

export function MonoText({
  children,
  className,
  title,
}: {
  children: ReactNode
  className?: string
  title?: string
}) {
  return (
    <span className={cn('truncate font-mono text-[12px] text-foreground', className)} title={title}>
      {children}
    </span>
  )
}

export function CodeChip({ children, className }: { children: ReactNode; className?: string }) {
  return <code className={cn('code-chip', className)}>{children}</code>
}

/** 弱化的占位符，用于「—」「无需」这类空值，避免显示成 0。 */
export function Dash() {
  return <span className="text-muted-foreground">—</span>
}
