import type { ReactNode } from 'react'

import { EmptyState, ErrorState } from './States'
import {
  Table,
  TableBody,
  TableHeader,
  TableSkeletonRows,
  TableStateRow,
} from '@/components/ui/table'

export interface TableFrameProps {
  /** 表头行（一排 `<TableHead>`）。加载 / 空 / 错误态下都保留。 */
  head: ReactNode
  /** 表头列数，状态行靠它算 colSpan。 */
  colCount: number
  /** 有数据时的行。 */
  children: ReactNode
  loading?: boolean
  /** 骨架行数，默认 6。 */
  skeletonRows?: number
  error?: { title: string; error: unknown; onRetry?: () => void }
  /** 走空态的条件由调用方判定（通常是 `total === 0`）。 */
  isEmpty?: boolean
  /** 空态内容，默认给一句「暂无数据」。 */
  empty?: ReactNode
  /** 帧内页脚，分页条放这里 —— 不要再往外面包一层壳。 */
  footer?: ReactNode
  /** 宽表在这里声明 min-width，例如 `min-w-[1080px]`。 */
  tableClassName?: string
  wrapClassName?: string
}

/**
 * 表格模板。
 *
 * 页面只声明「表头 + 数据 + 状态」，不写 `<thead>` / `<tbody>` / 外壳，
 * 因此不会出现「有人包了壳、有人没包」「空态三种形态」这类分叉。
 *
 * 这里刻意**不做列声明式配置** —— 列内容仍是调用方写的 JSX，
 * 模板只管结构：外壳单层、状态位内建、表头常驻、页脚进帧。
 */
export function TableFrame({
  head,
  colCount,
  children,
  loading,
  skeletonRows = 6,
  error,
  isEmpty,
  empty,
  footer,
  tableClassName,
  wrapClassName,
}: TableFrameProps) {
  const body = loading ? (
    <TableSkeletonRows rows={skeletonRows} cols={colCount} />
  ) : error ? (
    <TableStateRow colSpan={colCount}>
      <div className="p-6">
        <ErrorState title={error.title} error={error.error} onRetry={error.onRetry} />
      </div>
    </TableStateRow>
  ) : isEmpty ? (
    <TableStateRow colSpan={colCount}>
      {empty ?? <EmptyState title="暂无数据" description="当前条件下没有任何记录。" />}
    </TableStateRow>
  ) : (
    children
  )

  return (
    <Table className={tableClassName} wrapClassName={wrapClassName} footer={footer}>
      <TableHeader>{head}</TableHeader>
      <TableBody>{body}</TableBody>
    </Table>
  )
}
