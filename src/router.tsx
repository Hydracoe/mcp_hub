import { createBrowserRouter, Navigate } from 'react-router-dom'

import { AppShell } from '@/components/layout/AppShell'
import { OverviewPage } from '@/features/overview/OverviewPage'
import { ServersPage } from '@/features/servers/ServersPage'
import { ServerDetailPage } from '@/features/servers/ServerDetailPage'
import { CredentialsPage } from '@/features/credentials/CredentialsPage'
import { KeysPage } from '@/features/keys/KeysPage'
import { CategoriesPage } from '@/features/categories/CategoriesPage'
import { ToolsPage } from '@/features/tools/ToolsPage'
import { LogsPage } from '@/features/logs/LogsPage'
import { ConnectPage } from '@/features/connect/ConnectPage'
import { NotFoundPage } from '@/features/common/NotFoundPage'

/**
 * 路由表。
 *
 * 全部走同一个 AppShell（侧边栏 + 工具栏 + 页脚），
 * 页面只负责内容 —— 设计稿把外壳复制到 10 个文件里，
 * 于是版本号与页脚端点清单在各页之间对不上。
 */
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
