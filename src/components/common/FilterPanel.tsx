import type { ReactNode } from 'react'

import { Icon } from './Icon'
import { Label } from '@/components/ui/label'
import { cn } from '@/lib/utils'

export interface FilterSummaryItem {
  key: string
  label: string
}

/** 筛选卡片：所有表格页的查询条件收在一张卡里，底部分隔线右侧放不可逆动作。 */
export function FilterPanel({
  children,
  footer,
  footerEnd,
}: {
  children: ReactNode
  /** 底部行左侧：统计、刷新这类可逆信息与动作。 */
  footer?: ReactNode
  /** 底部行右侧：清空/删除这类不可逆动作。 */
  footerEnd?: ReactNode
}) {
  return (
    <div className="ui-panel">
      <div className="p-4">{children}</div>
      {footer || footerEnd ? (
        <div className="flex flex-wrap items-center justify-between gap-x-4 gap-y-2 border-t border-separator px-4 py-2.5">
          <div className="flex flex-wrap items-center gap-3">{footer}</div>
          {footerEnd}
        </div>
      ) : null}
    </div>
  )
}

/* 列数写死成类名，Tailwind 扫不到动态拼出来的字符串。 */
const COLUMNS: Record<number, string> = {
  1: 'xl:grid-cols-1',
  2: 'xl:grid-cols-2',
  3: 'xl:grid-cols-3',
  4: 'xl:grid-cols-4',
  5: 'xl:grid-cols-5',
}

/** 条件行：手机一列、平板两列、宽屏按 `columns` 铺开。 */
export function FilterGrid({
  columns = 4,
  className,
  children,
}: {
  columns?: number
  className?: string
  children: ReactNode
}) {
  return (
    <div
      className={cn(
        'grid grid-cols-1 gap-3 md:grid-cols-2',
        COLUMNS[columns] ?? COLUMNS[4],
        className,
      )}
    >
      {children}
    </div>
  )
}

/** 单个条件：标签 + 控件。`min-w-0` 防止下拉里的长文本把栅格撑破。 */
export function FilterField({
  label,
  className,
  children,
}: {
  label: ReactNode
  className?: string
  children: ReactNode
}) {
  return (
    <div className={cn('min-w-0', className)}>
      <Label className="mb-1.5">{label}</Label>
      {children}
    </div>
  )
}

/** 搜索行：搜索框占满，右侧留给「重置」这类可逆动作。 */
export function FilterSearch({ className, children }: { className?: string; children: ReactNode }) {
  return (
    <div className={cn('mt-3 flex flex-col gap-3 sm:flex-row sm:items-center', className)}>
      {children}
    </div>
  )
}

/** 已选条件摘要：点单个 chip 撤掉该条件，末尾可一键清空。 */
export function FilterSummary({
  items,
  onRemove,
  onClear,
}: {
  items: readonly FilterSummaryItem[]
  onRemove: (key: string) => void
  onClear: () => void
}) {
  if (items.length === 0) return null

  return (
    <div className="mt-3 flex flex-wrap items-center gap-2">
      <span className="text-xs text-muted-foreground">当前筛选：</span>
      {items.map((item) => (
        <button
          key={item.key}
          type="button"
          onClick={() => onRemove(item.key)}
          className="inline-flex items-center gap-1 rounded-full border border-separator bg-fill-secondary px-2 py-0.5 text-[11.5px] text-foreground hover:bg-fill-tertiary"
        >
          <span className="font-mono">{item.label}</span>
          <Icon name="x" size={12} className="text-icon-muted" />
        </button>
      ))}
      <button type="button" onClick={onClear} className="text-[11.5px] text-link hover:underline">
        清除全部
      </button>
    </div>
  )
}
