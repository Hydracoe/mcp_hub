import { Icon } from './Icon'
import { TableHead } from '@/components/ui/table'
import { cn } from '@/lib/utils'

export type SortDir = 'asc' | 'desc'

export interface SortHeadProps<K extends string> {
  label: string
  /** 这一列对应的排序键。 */
  col: K
  sort: K
  dir: SortDir
  /** 传 `undefined` 则这一列不可排，只渲染文案。 */
  onSort?: (col: K) => void
  className?: string
  align?: 'left' | 'right'
}

/** 可排序表头。控件长在表头上（不在筛选栏另放排序下拉，避免两份状态同步）；激活态只改箭头、不改文字色，未激活列常驻淡箭头提示可点。 */
export function SortHead<K extends string>({
  label,
  col,
  sort,
  dir,
  onSort,
  className,
  align = 'left',
}: SortHeadProps<K>) {
  const active = sort === col
  const ariaSort = active ? (dir === 'asc' ? 'ascending' : 'descending') : 'none'

  if (!onSort) {
    return (
      <TableHead className={cn(align === 'right' && 'text-right', className)} aria-sort={ariaSort}>
        {label}
      </TableHead>
    )
  }

  return (
    <TableHead className={cn(align === 'right' && 'text-right', className)} aria-sort={ariaSort}>
      <button
        type="button"
        onClick={() => onSort(col)}
        /* 提示词带上当前方向，读屏用户点之前就知道会发生什么 */
        title={active ? `当前${dir === 'asc' ? '升序' : '降序'}，点击切换为${dir === 'asc' ? '降序' : '升序'}` : `按${label}排序`}
        className="inline-flex cursor-pointer items-center gap-1 rounded-sm transition-colors hover:text-foreground"
      >
        {label}
        <Icon
          name={active && dir === 'asc' ? 'chevron-up' : 'chevron-down'}
          size={12}
          className={cn('shrink-0', active ? 'text-brand' : 'opacity-50')}
        />
      </button>
    </TableHead>
  )
}
