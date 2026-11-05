import { NavLink } from 'react-router-dom'

import { Icon } from '@/components/common/Icon'
import { Button } from '@/components/ui/button'
import { NAV_GROUPS } from './nav-config'
import { useUiStore } from '@/stores/ui-store'
import type { BackendConnection } from '@/lib/api/queries'

/** 连接三态 → 底部状态点与文案。说的是"前端连上后端了吗"，
 *  与"网关有没有工具可提供"无关——空库不该显示成故障。 */
const FOOT_VIEW = {
  connected: { dot: 'ok', label: '网关运行中' },
  connecting: { dot: 'idle', label: '连接中…' },
  disconnected: { dot: 'error', label: '无法连接后端' },
} as const

/** 侧边栏。材质、选中态、导航项高度全部来自 `.app-sidebar` / `.nav-item`，
 *  这里不重复写样式——同一处视觉有两个定义迟早分叉。 */
export function Sidebar({
  version,
  connection,
}: {
  version?: string
  /** 后端连接状态。决定底部状态点与文案。 */
  connection: BackendConnection
}) {
  const collapsed = useUiStore((s) => s.sidebarCollapsed)
  const toggleSidebar = useUiStore((s) => s.toggleSidebar)
  const foot = FOOT_VIEW[connection]

  return (
    <aside className="app-sidebar" data-collapsed={collapsed}>
      <div className="sidebar-head">
        <div className="sidebar-brand">
          <span className="brand-mark" aria-hidden="true">
            <Icon name="radio" size={15} />
          </span>
          <span className="brand-text min-w-0">
            <span className="brand-name block">MCP Hub</span>
            <span className="brand-sub block">聚合网关管理</span>
          </span>
          {/* 折叠开关：列表页列多、232px 侧边栏会把表格挤成横向滚动，这里补一个可收起入口。 */}
          <Button
            size="icon"
            variant="ghost"
            className="sidebar-collapse-btn ml-auto"
            aria-label={collapsed ? '展开侧边栏' : '收起侧边栏'}
            aria-expanded={!collapsed}
            onClick={toggleSidebar}
          >
            <Icon name={collapsed ? 'chevron-right' : 'chevron-left'} size={14} />
          </Button>
        </div>
      </div>

      <nav className="sidebar-nav" aria-label="主导航">
        {NAV_GROUPS.map((group) => (
          <div key={group.label} className="contents">
            <p className="nav-group-label">{group.label}</p>
            {group.items.map((item) => (
              // 选中态由 NavLink 输出的 aria-current="page" 驱动，apple-ui.css 的 `.nav-item[aria-current="page"]` 认它。
              <NavLink key={item.key} to={item.path} data-nav-key={item.key} className="nav-item">
                <Icon name={item.icon} size={16} className="nav-icon" />
                <span>{item.label}</span>
              </NavLink>
            ))}
          </div>
        ))}
      </nav>

      <div className="sidebar-foot">
        <span className="status-dot" data-state={foot.dot} />
        <span>{foot.label}</span>
        <span className="foot-meta">{version || '—'}</span>
      </div>
    </aside>
  )
}
