import { NavLink } from 'react-router-dom'

import { Icon } from '@/components/common/Icon'
import { Button } from '@/components/ui/button'
import { NAV_GROUPS } from './nav-config'
import { useUiStore } from '@/stores/ui-store'

/**
 * 侧边栏。
 *
 * 材质（半透明 + backdrop-filter）、选中态染色、导航项高度全部来自
 * apple-ui.css 的 `.app-sidebar` / `.nav-item`，这里不重复写样式 ——
 * 设计稿里导航项的圆角与卡片圆角曾经各自演化出两个值，
 * 就是因为同一处视觉有两个定义。
 */
export function Sidebar({
  version,
  running,
}: {
  version?: string
  /** 网关是否就绪。决定底部状态点与文案。 */
  running: boolean
}) {
  const collapsed = useUiStore((s) => s.sidebarCollapsed)
  const toggleSidebar = useUiStore((s) => s.toggleSidebar)

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
          {/* 折叠开关。设计稿没有折叠态：列表页列多，232px 的侧边栏
              会把表格挤成横向滚动，这里补一个可收起的入口。 */}
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
              // 选中态由 NavLink 输出的 aria-current="page" 驱动，
              // apple-ui.css 的 `.nav-item[aria-current="page"]` 认它。
              <NavLink key={item.key} to={item.path} data-nav-key={item.key} className="nav-item">
                <Icon name={item.icon} size={16} className="nav-icon" />
                <span>{item.label}</span>
              </NavLink>
            ))}
          </div>
        ))}
      </nav>

      <div className="sidebar-foot">
        <span className="status-dot" data-state={running ? 'ok' : 'error'} />
        <span>{running ? '网关运行中' : '网关未就绪'}</span>
        <span className="foot-meta">{version || '—'}</span>
      </div>
    </aside>
  )
}
