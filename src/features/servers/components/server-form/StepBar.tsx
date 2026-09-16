import { Icon } from '@/components/common/Icon'
import { cn } from '@/lib/utils'

import { WIZARD_STEPS } from './form-model'

/** 三步进度条。已完成的步骤显示对勾，当前步高亮，未到的置灰。 */
export function StepBar({ step }: { step: number }) {
  return (
    <div className="flex items-center gap-2">
      {WIZARD_STEPS.map((label, i) => {
        const n = i + 1
        const done = n < step
        return (
          <div key={label} className="flex flex-1 items-center gap-2">
            <span
              className={cn(
                'grid size-5 place-items-center rounded-full text-[11px] font-semibold',
                n === step && 'bg-foreground text-background',
                done && 'bg-success text-success-foreground',
                !done && n !== step && 'bg-fill-tertiary text-muted-foreground',
              )}
            >
              {done ? <Icon name="check" size={12} /> : n}
            </span>
            <span
              className={cn(
                'text-[11.5px]',
                n === step ? 'font-medium text-foreground' : 'text-muted-foreground',
              )}
            >
              {label}
            </span>
            {n < WIZARD_STEPS.length ? <span className="h-px flex-1 bg-separator" /> : null}
          </div>
        )
      })}
    </div>
  )
}
