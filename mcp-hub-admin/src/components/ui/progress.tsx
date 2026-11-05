import { Progress as ProgressPrimitive } from 'radix-ui'

import { cn } from '@/lib/utils'

export interface ProgressProps
  extends React.ComponentProps<typeof ProgressPrimitive.Root> {
  /** 0–100。 */
  value?: number
  /** 填充色，默认品牌蓝。传语义 class（如 `bg-success-strong`）即可换色。 */
  indicatorClassName?: string
}

/** 进度条。用设计系统的 `.progress-track`/`.progress-fill`（5px 高、全圆角），而非 shadcn 默认的 8px。 */
function Progress({ className, value, indicatorClassName, ...props }: ProgressProps) {
  const pct = Math.max(0, Math.min(100, value ?? 0))
  return (
    <ProgressPrimitive.Root
      data-slot="progress"
      value={pct}
      className={cn('progress-track', className)}
      {...props}
    >
      <ProgressPrimitive.Indicator
        data-slot="progress-indicator"
        className={cn('progress-fill bg-brand', indicatorClassName)}
        style={{ width: `${pct}%` }}
      />
    </ProgressPrimitive.Root>
  )
}

export { Progress }
