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

/**
 * 侧边栏导航。
 *
 * 「监控 / 诊断」两组与设计稿一致；「管理」组是本项目补的 ——
 * PRD §4.6 要求「密钥池」「工具浏览器」（UI 定名「工具市场」）各自可达，
 * 而设计稿把这两块
 * 做成了 Server 列表里的弹框，没有独立入口。这里保留弹框形态，
 * 同时给出可直达、可分享链接的路由。
 */
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
      // 分类排在工具浏览器之前：它决定工具名，是上游配置。
      { key: 'categories', label: '分类', path: '/categories', icon: 'folder' },
      { key: 'tools', label: '工具浏览器', path: '/tools', icon: 'wrench' },
      // 两个"密钥"入口是刻意的，且必须挨着 —— 它们是同一条链路的两端：
      // 「接入密钥」是上游客户端调网关的凭据（网关签发），
      // 「密钥池」是网关调下游 server 的凭据（按 Server 隔离）。
      // 图标也要分开，否则侧边栏上两行一模一样的钥匙等于没有区分。
      { key: 'keys', label: '接入密钥', path: '/keys', icon: 'shield-check' },
      { key: 'credentials', label: '密钥池', path: '/credentials', icon: 'key-round' },
    ],
  },
]
