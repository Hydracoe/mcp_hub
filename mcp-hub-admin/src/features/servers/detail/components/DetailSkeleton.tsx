import { CardSkeleton } from '@/components/common/States'
import { Skeleton } from '@/components/ui/skeleton'

/** 详情页加载骨架（表单，不是表格）。行高与左右分栏比例都要照抄真实布局，
 *  否则加载完会跳动、横移。 */
export function DetailSkeleton() {
  return (
    <div className="grid grid-cols-1 gap-6 lg:grid-cols-3">
      <div className="lg:col-span-2 min-w-0">
        <div className="form-card space-y-5 p-5">
          {Array.from({ length: 8 }).map((_, i) => (
            <Skeleton key={i} className="h-9 w-full" />
          ))}
        </div>
      </div>
      <div className="lg:col-span-1 min-w-0 space-y-4">
        <CardSkeleton count={2} />
      </div>
    </div>
  )
}
