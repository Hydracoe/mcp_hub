import { Suspense } from 'react'
import { Outlet, useLocation } from 'react-router-dom'

import { Sidebar } from './Sidebar'
import { RouteErrorBoundary } from './RouteErrorBoundary'
import { PageSkeleton } from '@/components/common/States'
import { useBackendConnection, useOverview } from '@/lib/api/queries'
import { cn } from '@/lib/utils'

/** 应用外壳：侧边栏 + 主区（工具栏/内容）。
 *  `is-fixed` 用于主区独立滚动的页（Server 详情），由布局层判断。
 *  错误边界挂这里而非更外层：chunk 下载失败时侧边栏/工具栏仍可交互。 */
export function AppShell() {
  const { data } = useOverview()
  const connection = useBackendConnection()
  const { pathname } = useLocation()

  const isFixed = /^\/servers\/[^/]+$/.test(pathname)

  return (
    <div className={cn('app-shell', isFixed && 'is-fixed')}>
      <Sidebar version={data?.version} connection={connection} />
      <div className={cn('app-main', isFixed && 'is-fixed')}>
        <RouteErrorBoundary resetKey={pathname}>
          <Suspense fallback={<PageSkeleton />}>
            <Outlet />
          </Suspense>
        </RouteErrorBoundary>
      </div>
    </div>
  )
}
