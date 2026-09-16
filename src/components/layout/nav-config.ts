import type { IconName } from '@/components/common/icon-names'

export interface NavItem {
  key: string
  label: string
  path: string
  icon: IconName
}

export interface NavGroup {
  label: string
  items: NavItem[]
}

/** 侧边栏导航。 */
export const NAV_GROUPS: NavGroup[] = [
  {
    label: '监控',
    items: [
      { key: 'overview', label: '概览', path: '/overview', icon: 'layout-dashboard' },
      { key: 'servers', label: 'Server 列表', path: '/servers', icon: 'server' },
    ],
  },
  {
    label: '诊断',
    items: [
      { key: 'logs', label: '调用日志', path: '/logs', icon: 'scroll-text' },
      { key: 'connect', label: '接入指引', path: '/connect', icon: 'plug' },
    ],
  },
  {
    label: '管理',
    items: [
      // 分类决定工具名，是上游配置，所以排在工具管理之前。
      { key: 'categories', label: '分类', path: '/categories', icon: 'folder' },
      { key: 'tools', label: '工具管理', path: '/tools', icon: 'wrench' },
      // 两个「密钥」必须挨着：同一条链路的两端——「接入密钥」是上游调网关的凭据，「密钥池」是网关调下游的凭据（按 Server 隔离）；图标也要分开，否则两行钥匙没区分。
      { key: 'keys', label: '接入密钥', path: '/keys', icon: 'shield-check' },
      { key: 'credentials', label: '密钥池', path: '/credentials', icon: 'key-round' },
    ],
  },
]
