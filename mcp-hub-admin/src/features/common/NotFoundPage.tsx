import { Link } from 'react-router-dom'

import { Icon } from '@/components/common/Icon'
import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'

/** 404。侧边栏仍在，用户能从左边直接跳回去。 */
export function NotFoundPage() {
  return (
    <div className="app-content">
      <Card className="p-10">
        <div className="flex flex-col items-center gap-2 text-center">
          <span className="grid size-11 place-items-center rounded-xl bg-fill-tertiary text-icon-muted">
            <Icon name="circle-question-mark" size={22} />
          </span>
          <p className="text-[15px] font-semibold text-foreground">页面不存在</p>
          <p className="max-w-[46ch] text-[12px] leading-relaxed text-muted-foreground">
            该地址没有对应的页面。可能是链接过期，或者手输时路径写错了。
          </p>
          <Button asChild variant="secondary" className="mt-2">
            <Link to="/overview">
              <Icon name="layout-dashboard" size={14} />
              回到概览
            </Link>
          </Button>
        </div>
      </Card>
    </div>
  )
}
