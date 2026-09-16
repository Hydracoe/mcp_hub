import * as React from 'react'

import { cn } from '@/lib/utils'

/** 卡片/面板外壳。⚠ 与 shadcn 不同：不给 padding/gap，一律由调用方给——内边距五花八门（p-4/p-5/p-6/p-0），写进 Card 反而逼着到处覆盖。 */
function Card({ className, ...props }: React.ComponentProps<'div'>) {
  return <div data-slot="card" className={cn('ui-card', className)} {...props} />
}

/** 无阴影的平面容器（`.ui-panel`）。 */
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
