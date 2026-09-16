import { Outlet, useLocation } from 'react-router-dom'

import { Sidebar } from './Sidebar'
import { useOverview } from '@/lib/api/queries'
import { cn } from '@/lib/utils'

/**
 * 应用外壳。
 *
 * 结构固定为「侧边栏 + 主区（工具栏 / 内容）」。
 * 页面自己只负责内容，工具栏由这里统一给 ——
 * 否则每个页面都要把同一段外壳复制一遍。
 *
 * `is-fixed` 变体用于「主区独立滚动」的页面（Server 详情），
 * 由页面通过 `useAppShellMode` 声明。这里用 location.pathname 判断，
 * 因为它是布局层的决定，不该让子页面往父级塞状态。
 */
export function AppShell() {
  const { data, isError } = useOverview()
  const { pathname } = useLocation()

  const isFixed = /^\/servers\/[^/]+$/.test(pathname)

  return (
    <div className={cn('app-shell', isFixed && 'is-fixed')}>
      <Sidebar version={data?.version} running={Boolean(data?.ready) && !isError} />
      <div className={cn('app-main', isFixed && 'is-fixed')}>
        <Outlet />
      </div>
    </div>
  )
}
