import * as React from 'react'

import { cn } from '@/lib/utils'

/**
 * 卡片 / 面板外壳。
 *
 * ⚠ 与 shadcn 默认不同：这里**不给** padding 与 gap。
 * 设计稿里的卡片有 `p-5`（指标卡）、`p-6`（配置面板）、`p-4`（筛选面板）
 * 三种内边距，还有 `p-0`（表格容器）这种用法。把 padding 写进 Card
 * 会逼着每个调用点再写一遍 `p-0!` 去覆盖它 —— 设计稿里
 * 「有的卡片内边距忽然变宽」就是这么来的。内边距一律由调用方给。
 */
function Card({ className, ...props }: React.ComponentProps<'div'>) {
  return <div data-slot="card" className={cn('ui-card', className)} {...props} />
}

/** 无阴影的平面容器（设计稿的 `.ui-panel`）。 */
function Panel({ className, ...props }: React.ComponentProps<'div'>) {
  return <div data-slot="panel" className={cn('ui-panel', className)} {...props} />
}

function CardHeader({ className, ...props }: React.ComponentProps<'div'>) {
  return (
    <div
      data-slot="card-header"
      className={cn('flex items-start justify-between gap-3', className)}
      {...props}
    />
  )
}

function CardTitle({ className, ...props }: React.ComponentProps<'div'>) {
  return <div data-slot="card-title" className={cn('h2', className)} {...props} />
}

function CardDescription({ className, ...props }: React.ComponentProps<'div'>) {
  return (
    <div
      data-slot="card-description"
      className={cn('text-[11px] text-muted-foreground', className)}
      {...props}
    />
  )
}

function CardContent({ className, ...props }: React.ComponentProps<'div'>) {
  return <div data-slot="card-content" className={cn(className)} {...props} />
}

function CardFooter({ className, ...props }: React.ComponentProps<'div'>) {
  return (
    <div
      data-slot="card-footer"
      className={cn('flex items-center gap-2', className)}
      {...props}
    />
  )
}

export { Card, Panel, CardHeader, CardTitle, CardDescription, CardContent, CardFooter }
