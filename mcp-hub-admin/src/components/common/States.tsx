import type { ReactNode } from 'react'

import { Icon, type IconName } from './Icon'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { cn } from '@/lib/utils'

/* ────────────────────────────── 空状态 ────────────────────────────── */

export interface EmptyStateProps {
  icon?: IconName
  title: string
  description?: ReactNode
  action?: ReactNode
  className?: string
}

export function EmptyState({ icon = 'box', title, description, action, className }: EmptyStateProps) {
  return (
    <div className={cn('flex flex-col items-center gap-2 px-6 py-14 text-center', className)}>
      <span className="mb-1 grid size-10 place-items-center rounded-xl bg-fill-tertiary text-icon-muted">
        <Icon name={icon} size={20} />
      </span>
      <p className="text-[13px] font-semibold text-foreground">{title}</p>
      {description ? (
        <p className="max-w-[46ch] text-[12px] leading-relaxed text-muted-foreground">{description}</p>
      ) : null}
      {action ? <div className="mt-2">{action}</div> : null}
    </div>
  )
}

/* ────────────────────────────── 错误 ────────────────────────────── */

export interface ErrorStateProps {
  title?: string
  error?: unknown
  onRetry?: () => void
  className?: string
}

/** 失败态。把后端给的中文 error 原样展示 —— 那一句话往往就是排查方向。 */
export function ErrorState({ title = '加载失败', error, onRetry, className }: ErrorStateProps) {
  const message =
    error instanceof Error ? error.message : typeof error === 'string' ? error : '未知错误'
  const code =
    error && typeof error === 'object' && 'code' in error
      ? String((error as { code: unknown }).code)
      : undefined

  return (
    <div
      className={cn(
        'flex flex-col items-center gap-2 rounded-xl border border-error/25 bg-error-surface px-6 py-10 text-center',
        className,
      )}
    >
      <span className="grid size-10 place-items-center rounded-xl bg-error/12 text-error-strong">
        <Icon name="triangle-alert" size={20} />
      </span>
      <p className="text-[13px] font-semibold text-foreground">{title}</p>
      <p className="max-w-[52ch] font-mono text-[12px] leading-relaxed text-error-strong">
        {message}
      </p>
      {code ? <p className="font-mono text-[11px] text-muted-foreground">code = {code}</p> : null}
      {onRetry ? (
        <Button variant="secondary" size="sm" className="mt-2" onClick={onRetry}>
          <Icon name="refresh-cw" size={13} />
          重试
        </Button>
      ) : null}
    </div>
  )
}

/* ────────────────────────────── 加载 ────────────────────────────── */

/** 表格加载骨架不在这里，见 `ui/table.tsx` 的 `TableSkeletonRows`：
 *  它由 TableFrame 渲染在真实表头下面，行高与真实行一致。 */

/** 指标卡骨架。 */
export function CardSkeleton({ count = 4 }: { count?: number }) {
  return (
    <div className="stat-grid">
      {Array.from({ length: count }).map((_, i) => (
        <div key={i} className="ui-card p-5">
          <Skeleton className="h-3 w-24" />
          <Skeleton className="mt-4 h-7 w-32" />
          <Skeleton className="mt-4 h-3 w-full" />
          <Skeleton className="mt-3 h-3 w-2/3" />
        </div>
      ))}
    </div>
  )
}

/** 行内加载：按钮里的转圈。 */
export function Spinner({ size = 14, className }: { size?: number; className?: string }) {
  return <Icon name="refresh-cw" size={size} className={cn('animate-spin', className)} />
}

/** 路由级懒加载占位：只复刻工具栏与内容区的外框尺寸，不画假页面骨架——页面形状各异，猜错反而更晃眼。 */
export function PageSkeleton() {
  return (
    <>
      <div className="app-toolbar">
        <Skeleton className="h-4 w-32" />
      </div>
      <div className="app-content">
        <div className="ui-panel p-6">
          <Skeleton className="h-3 w-28" />
          <Skeleton className="mt-5 h-9 w-full max-w-md" />
          <Skeleton className="mt-3 h-9 w-full" />
          <Skeleton className="mt-3 h-9 w-full" />
          <Skeleton className="mt-3 h-9 w-3/4" />
        </div>
      </div>
    </>
  )
}
