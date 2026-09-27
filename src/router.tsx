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

/** 路由表：全部走同一个 AppShell（侧边栏/工具栏/页脚），页面只负责内容，避免外壳散落各页导致版本号、页脚对不上。
 *  页面组件在 `@/routes/lazy-pages`，本文件只导出 `router` 这一个非组件值。
 *
 *  用 Hash 模式（`#/overview`）而不是 History 模式，是为了让同一份构建产物
 *  同时服务于两种运行形态：
 *   - Web 部署：后端 HTTP 服务只在 `/api`、`/mcp` 上有路由，`/overview`
 *     这种路径没有任何服务端兜底；History 模式下刷新页面就是 404。
 *   - 桌面端：前端资源由内嵌的静态资源服务器提供（Wails 的
 *     AssetFileServerFS 本质是 http.FileServer，同样不做 SPA 回退），
 *     深层链接一样会 404。
 *
 *  Hash 模式把路由状态放在 `#` 之后，浏览器与资源服务器都只看到 `/`，
 *  于是两种形态都不需要任何服务端配置。代价是 URL 里多一个 `#`——
 *  对一个本机管理后台来说，这个代价是零。
 *
 *  （另一条路是保留 History 模式、由每个服务端各加一次 SPA 回退。
 *  那要求 Web 侧的部署者（nginx / vite preview）和桌面侧的资产服务器
 *  都记得配，一处漏配就是刷新 404——把"能不能刷新"变成部署配置的责任，
 *  不值得。） */
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
