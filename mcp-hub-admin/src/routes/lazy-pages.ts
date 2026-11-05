import { lazy } from 'react'

/**
 * 路由级懒加载的页面，每个页面单独一个 chunk。
 *
 * 不写在 `router.tsx` 里：那边导出的是非组件值，和 lazy 组件混在一处
 * 会让 Fast Refresh 无法判断能否热替换。页面模块都是命名导出，
 * 这里多一层 `.then` 适配 `lazy` 要求的 default。
 *
 * `NotFoundPage` 不在这里 —— 它只有几十行，单独成 chunk 只是让兜底页
 * 多一次网络往返。
 */
export const OverviewPage = lazy(() =>
  import('@/features/overview/OverviewPage').then((m) => ({ default: m.OverviewPage })),
)
export const ServersPage = lazy(() =>
  import('@/features/servers/ServersPage').then((m) => ({ default: m.ServersPage })),
)
export const ServerDetailPage = lazy(() =>
  import('@/features/servers/ServerDetailPage').then((m) => ({ default: m.ServerDetailPage })),
)
export const CredentialsPage = lazy(() =>
  import('@/features/credentials/CredentialsPage').then((m) => ({ default: m.CredentialsPage })),
)
export const KeysPage = lazy(() =>
  import('@/features/keys/KeysPage').then((m) => ({ default: m.KeysPage })),
)
export const CategoriesPage = lazy(() =>
  import('@/features/categories/CategoriesPage').then((m) => ({ default: m.CategoriesPage })),
)
export const ToolsPage = lazy(() =>
  import('@/features/tools/ToolsPage').then((m) => ({ default: m.ToolsPage })),
)
export const LogsPage = lazy(() =>
  import('@/features/logs/LogsPage').then((m) => ({ default: m.LogsPage })),
)
export const ConnectPage = lazy(() =>
  import('@/features/connect/ConnectPage').then((m) => ({ default: m.ConnectPage })),
)
