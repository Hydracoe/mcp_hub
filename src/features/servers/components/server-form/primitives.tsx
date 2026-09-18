import type { ComponentProps, ReactNode } from 'react'

import { Icon, type IconName } from '@/components/common/Icon'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { cn } from '@/lib/utils'

/** 向导内部的排版原语，多处复用，避免标记分叉。 */

/** 左侧标签 + 右侧控件的横向行（开关、单选这类）。 */
export function Row({
  label,
  hint,
  children,
}: {
  label: string
  hint?: string
  children: ReactNode
}) {
  return (
    <div className="flex items-center justify-between gap-3">
      <div>
        <Label className="mb-0">{label}</Label>
        {hint ? <p className="form-hint mt-0">{hint}</p> : null}
      </div>
      {children}
    </div>
  )
}

/** 字段的错误提示。`message` 为空时渲染 `fallback`（通常是用法说明）。 */
export function FieldHint({ message, fallback }: { message?: string; fallback?: ReactNode }) {
  if (message) return <p className="form-hint text-error-strong">{message}</p>
  return fallback ? <p className="form-hint">{fallback}</p> : null
}

const CALLOUT_TONE = {
  info: { icon: 'info', color: 'text-icon-muted' },
  warning: { icon: 'triangle-alert', color: 'text-warning-strong' },
} as const satisfies Record<string, { icon: IconName; color: string }>

/** 表单里的说明块。用于「为什么这里不出现某个字段」这类需要解释的地方。 */
export function Callout({
  tone = 'info',
  children,
  className,
}: {
  tone?: keyof typeof CALLOUT_TONE
  children: ReactNode
  className?: string
}) {
  const { icon, color } = CALLOUT_TONE[tone]
  return (
    <p
      className={cn(
        'flex items-start gap-2 rounded-lg border border-separator bg-fill-tertiary p-3 text-[12px] text-foreground',
        className,
      )}
    >
      <Icon name={icon} size={15} className={cn('mt-0.5 shrink-0', color)} />
      <span>{children}</span>
    </p>
  )
}

/** 两段式切换按钮组（「需要 / 不需要」）。 */
export function SegmentedToggle<T extends string>({
  value,
  options,
  onChange,
  'aria-label': ariaLabel,
}: {
  value: T
  options: readonly { value: T; label: string }[]
  onChange: (value: T) => void
  'aria-label'?: string
}) {
  return (
    <div
      role="group"
      aria-label={ariaLabel}
      className="flex gap-1 rounded-lg border border-separator p-0.5"
    >
      {options.map((o) => (
        <button
          key={o.value}
          type="button"
          aria-pressed={value === o.value}
          onClick={() => onChange(o.value)}
          className={cn(
            'rounded-md px-2.5 py-1 text-[12px]',
            value === o.value ? 'bg-foreground text-background' : 'text-muted-foreground',
          )}
        >
          {o.label}
        </button>
      ))}
    </div>
  )
}

/** 带单位后缀的数字输入（如 Timeout 的 ms）。 */
export function NumberField({
  unit,
  className,
  ...props
}: ComponentProps<typeof Input> & { unit: string }) {
  return (
    <div className={cn('relative', className)}>
      <Input {...props} type="number" className={unit === 'ms' ? 'pr-12' : 'pr-16'} />
      <span className="pointer-events-none absolute right-3 top-1/2 -translate-y-1/2 text-[12px] text-muted-foreground">
        {unit}
      </span>
    </div>
  )
}
