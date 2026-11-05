import * as React from 'react'

import { Skeleton } from '@/components/ui/skeleton'
import { cn } from '@/lib/utils'

/**
 * 表格原语。视觉全部来自 `.ui-table` / `.ui-table-wrap`，结构固定为：
 * 帧（背景/描边/圆角/阴影）→ 横向滚动层 → table.ui-table → 可选页脚。
 *
 * 外壳由 `Table` 单独负责：外面不要再包 `.ui-table-wrap`，分页条走 `footer`，
 * 否则会出现双描边、双圆角、双滚动条。
 * 单元格字号由 CSS 单点决定，组件里不要再叠 `text-sm` / `text-xs`，
 * 需要第二行小字用 `<span className="cell-sub">`。
 */
function Table({
  className,
  wrapClassName,
  footer,
  ...props
}: React.ComponentProps<'table'> & {
  wrapClassName?: string
  /** 帧内页脚，分页条走这里。 */
  footer?: React.ReactNode
}) {
  return (
    <div data-slot="table-container" className={cn('ui-table-wrap', wrapClassName)}>
      <div className="ui-table-scroll">
        <table data-slot="table" className={cn('ui-table', className)} {...props} />
      </div>
      {footer}
    </div>
  )
}

/** 不带外壳的表格，用于已经处在带边框容器内的场景（弹框、表单卡）。 */
function BareTable({ className, ...props }: React.ComponentProps<'table'>) {
  return <table data-slot="table" className={cn('ui-table', className)} {...props} />
}

function TableHeader({ className, ...props }: React.ComponentProps<'thead'>) {
  return <thead data-slot="table-header" className={cn(className)} {...props} />
}

function TableBody({ className, ...props }: React.ComponentProps<'tbody'>) {
  return <tbody data-slot="table-body" className={cn(className)} {...props} />
}

function TableFooter({ className, ...props }: React.ComponentProps<'tfoot'>) {
  return <tfoot data-slot="table-footer" className={cn(className)} {...props} />
}

function TableRow({ className, ...props }: React.ComponentProps<'tr'>) {
  return <tr data-slot="table-row" className={cn(className)} {...props} />
}

function TableHead({ className, ...props }: React.ComponentProps<'th'>) {
  return <th data-slot="table-head" className={cn(className)} {...props} />
}

function TableCell({ className, ...props }: React.ComponentProps<'td'>) {
  return <td data-slot="table-cell" className={cn(className)} {...props} />
}

function TableCaption({ className, ...props }: React.ComponentProps<'caption'>) {
  return (
    <caption
      data-slot="table-caption"
      className={cn('mt-4 text-[11.5px] text-muted-foreground', className)}
      {...props}
    />
  )
}

/** 状态行：空态/错误态都走它，并保留表头——否则用户看不到列的构成，不知「空」的是什么形状。 */
function TableStateRow({
  colSpan,
  className,
  children,
  ...props
}: React.ComponentProps<'td'> & { colSpan: number }) {
  return (
    <tr data-slot="table-state-row" className="is-static is-state">
      <td colSpan={colSpan} className={className} {...props}>
        {children}
      </td>
    </tr>
  )
}

/** 加载骨架行。行高与真实行一致（11px/16px），加载完不跳动。 */
function TableSkeletonRows({ rows = 6, cols = 6 }: { rows?: number; cols?: number }) {
  return (
    <>
      {Array.from({ length: rows }).map((_, r) => (
        <tr key={r} data-slot="table-skeleton-row" className="is-static">
          {Array.from({ length: cols }).map((_, c) => (
            <td key={c}>
              <Skeleton className="h-4 w-full" style={{ opacity: 1 - r * 0.08 }} />
            </td>
          ))}
        </tr>
      ))}
    </>
  )
}

export {
  Table,
  BareTable,
  TableHeader,
  TableBody,
  TableFooter,
  TableRow,
  TableHead,
  TableCell,
  TableCaption,
  TableStateRow,
  TableSkeletonRows,
}
