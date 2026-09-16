import { Icon } from './Icon'
import { TableHead } from '@/components/ui/table'
import { cn } from '@/lib/utils'

export type SortDir = 'asc' | 'desc'

export interface SortHeadProps<K extends string> {
  /** 表头文案。 */
  label: string
  /** 这一列对应的排序键。 */
  col: K
  /** 当前排序列。 */
  sort: K
  dir: SortDir
  /** 点这一列时回调。传 `undefined` 则这一列不可排，只渲染文案。 */
  onSort?: (col: K) => void
  className?: string
  align?: 'left' | 'right'
}

/**
 * 可排序表头。
 *
 * 排序能力长在**表头**上，而不是在筛选栏再塞一个「排序方式」下拉：
 * 表格自己的顺序，控件就该在表格上；两处入口意味着两份状态要互相同步。
 *
 * 三种状态刻意画得不一样：
 *   - 未激活：箭头常驻但淡（`text-icon-muted`），让人知道这列能点
 *   - 激活升序：箭头朝上 + 实色
 *   - 激活降序：箭头朝下 + 实色
 * 只画激活态的话，未激活的列看起来和纯文本表头一模一样，没人会发现能点。
 */
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
        className={cn(
          'inline-flex cursor-pointer items-center gap-1 rounded-sm transition-colors hover:text-foreground',
          active ? 'text-foreground' : 'text-icon-muted',
        )}
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
