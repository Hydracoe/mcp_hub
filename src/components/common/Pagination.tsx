import { Icon } from './Icon'
import { Button } from '@/components/ui/button'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { formatInt } from '@/lib/format'
import { cn } from '@/lib/utils'

export interface PaginationProps {
  page: number
  pageSize: number
  total: number
  onPageChange: (page: number) => void
  onPageSizeChange?: (size: number) => void
  /** 左侧文案的模板，「个 Server」这类量词由调用方给。 */
  unit?: string
  /**
   * 独立成框（不挂在表格帧内）。
   *
   * 默认形态只有一条上边线，是靠 `TableFrame` 的 `footer` 槽位活在表壳里的；
   * 单独摆一条分页时把这条边线换成整圈描边，否则顶边会出现两道线。
   */
  standalone?: boolean
  className?: string
}

const PAGE_SIZES = [10, 20, 50, 100]

/**
 * 分页条。
 *
 * 设计稿有两条互相打架的实现：`.pg-btn`（自带选中态样式）与
 * `ui-btn ui-btn-primary`（把选中态交给按钮变体）。两者同时挂在
 * 一个元素上时，谁赢取决于 CSS 顺序 —— 这里只用 `.pg-btn` 一条路。
 */
export function Pagination({
  page,
  pageSize,
  total,
  onPageChange,
  onPageSizeChange,
  unit = '条',
  standalone = false,
  className,
}: PaginationProps) {
  const pages = Math.max(1, Math.ceil(total / pageSize))
  const current = Math.min(page, pages)
  const from = total === 0 ? 0 : (current - 1) * pageSize + 1
  const to = Math.min(current * pageSize, total)

  return (
    <div
      className={cn(
        'flex flex-wrap items-center justify-between gap-3 px-4 py-2.5 text-xs text-muted-foreground',
        standalone
          ? 'rounded-xl border border-border bg-card'
          : 'border-t border-separator',
        className,
      )}
    >
      <div className="flex items-center gap-3">
        <span className="tabular-nums">
          显示 {formatInt(from)}-{formatInt(to)} / 共 {formatInt(total)} {unit}
        </span>
        {onPageSizeChange ? (
          /* 之前这里是原生 <select class="form-select h-6! …">：
             高度被 !important 压到 24px，箭头靠 CSS background-image 画，
             和页面里其它下拉不是一套。改用同一枚 Select 原语的 sm 档。 */
          <Select
            value={String(pageSize)}
            onValueChange={(v) => onPageSizeChange(Number(v))}
          >
            <SelectTrigger size="sm" className="w-auto" aria-label="每页行数">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {PAGE_SIZES.map((n) => (
                <SelectItem key={n} value={String(n)}>
                  {n} / 页
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        ) : null}
      </div>

      <div className="flex items-center gap-2">
        <Button
          size="icon"
          variant="ghost"
          aria-label="上一页"
          disabled={current <= 1}
          onClick={() => onPageChange(current - 1)}
        >
          <Icon name="chevron-left" size={14} />
        </Button>
        <span className="font-mono tabular-nums text-foreground">
          {current} / {pages}
        </span>
        <Button
          size="icon"
          variant="ghost"
          aria-label="下一页"
          disabled={current >= pages}
          onClick={() => onPageChange(current + 1)}
        >
          <Icon name="chevron-right" size={14} />
        </Button>
      </div>
    </div>
  )
}
