import { createBrowserRouter, Navigate } from 'react-router-dom'

import { AppShell } from '@/components/layout/AppShell'
import { NotFoundPage } from '@/features/common/NotFoundPage'
import {
  CategoriesPage,
  ConnectPage,
  CredentialsPage,
  KeysPage,
  LogsPage,
  OverviewPage,
  ServerDetailPage,
  ServersPage,
  ToolsPage,
} from '@/routes/lazy-pages'

/** 路由表：全部走同一个 AppShell（侧边栏/工具栏/页脚），页面只负责内容，避免外壳散落各页导致版本号、页脚对不上。
 *  页面组件在 `@/routes/lazy-pages`，本文件只导出 `router` 这一个非组件值。 */
export const router = createBrowserRouter([
  {
    path: '/',
    element: <AppShell />,
    children: [
      { index: true, element: <Navigate to="/overview" replace /> },
      { path: 'overview', element: <OverviewPage /> },
      { path: 'servers', element: <ServersPage /> },
      { path: 'servers/:id', element: <ServerDetailPage /> },
      { path: 'keys', element: <KeysPage /> },
      { path: 'credentials', element: <CredentialsPage /> },
      { path: 'categories', element: <CategoriesPage /> },
      { path: 'tools', element: <ToolsPage /> },
      { path: 'logs', element: <LogsPage /> },
      { path: 'connect', element: <ConnectPage /> },
      { path: '*', element: <NotFoundPage /> },
    ],
  },
])
