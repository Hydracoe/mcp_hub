import type { ReactNode } from 'react'

import { cn } from '@/lib/utils'

export interface SectionProps {
  title?: ReactNode
  hint?: ReactNode
  actions?: ReactNode
  /** 去掉标题行下方的间距（内容自带表头时用）。 */
  tight?: boolean
  className?: string
  children: ReactNode
}

/** 内容区块：标题行 + 主体。标题行统一走 `.section-head`。 */
export function Section({ title, hint, actions, tight, className, children }: SectionProps) {
  const hasHead = title || hint || actions
  return (
    <section className={className}>
      {hasHead ? (
        <div className={cn('section-head', tight && 'mb-2')}>
          <div className="flex items-center gap-3 min-w-0">
            {typeof title === 'string' ? <h2 className="truncate">{title}</h2> : title}
            {hint}
          </div>
          {actions ? <div className="flex items-center gap-2">{actions}</div> : null}
        </div>
      ) : null}
      {children}
    </section>
  )
}
