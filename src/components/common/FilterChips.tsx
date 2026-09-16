import { cn } from '@/lib/utils'

export interface FilterChipOption<T extends string> {
  value: T
  label: string
  /** 前面的小圆点颜色，用于「有冷却」「有不可用」这类语义筛选。 */
  tone?: 'success' | 'warning' | 'error' | 'neutral'
  count?: number
}

export interface FilterChipsProps<T extends string> {
  value: T
  options: readonly FilterChipOption<T>[]
  onChange: (value: T) => void
  /** `sm`（默认）用于列表页工具条；`md` 高度顶到 32px，用于和 Select/输入同排的表单条件行。 */
  size?: 'sm' | 'md'
  className?: string
}

const toneColor: Record<NonNullable<FilterChipOption<string>['tone']>, string> = {
  success: 'var(--state-success)',
  warning: 'var(--state-warning)',
  error: 'var(--state-error)',
  neutral: 'var(--icon-muted)',
}

/** 分段式筛选 chip。外观由 `value` 单向决定。 */
export function FilterChips<T extends string>({
  value,
  options,
  onChange,
  size = 'sm',
  className,
}: FilterChipsProps<T>) {
  return (
    <div className={cn('flex flex-wrap items-center gap-1.5 text-xs', className)} role="group">
      {options.map((opt) => {
        const active = opt.value === value
        return (
          <button
            key={opt.value}
            type="button"
            aria-pressed={active}
            onClick={() => onChange(opt.value)}
            className={cn(
              'inline-flex items-center gap-1.5 rounded-full transition-colors',
              'cursor-pointer border-0 font-medium',
              size === 'md' ? 'h-8 px-3 text-[13px]' : 'px-2.5 py-1',
              active
                ? 'bg-foreground text-background font-semibold'
                : 'bg-muted text-foreground hover:bg-fill-secondary',
            )}
          >
            {opt.tone ? (
              <span
                className="size-2 shrink-0 rounded-full"
                style={{ background: toneColor[opt.tone] }}
              />
            ) : null}
            {opt.label}
            {opt.count !== undefined ? (
              <span className={cn('tabular-nums', active ? 'opacity-80' : 'opacity-60')}>
                {opt.count}
              </span>
            ) : null}
          </button>
        )
      })}
    </div>
  )
}
