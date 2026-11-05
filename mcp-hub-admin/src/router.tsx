import { createHashRouter, Navigate } from 'react-router-dom'

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

/** 路由表：统一走 AppShell，页面只管内容。用 Hash 模式（`#/overview`）而非 History：Web 后端只在 /api、/mcp 有路由，
 *  桌面端静态服务器也不做 SPA 回退，History 下刷新即 404；Hash 让两种部署都无需服务端配置，代价只是 URL 多一个 `#`。
 *  页面组件在 `@/routes/lazy-pages`，本文件只导出 `router` 这一个非组件值。 */
export const router = createHashRouter([
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
