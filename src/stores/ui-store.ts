import { create } from 'zustand'
import { persist } from 'zustand/middleware'

export type ThemeMode = 'light' | 'dark' | 'system'

/**
 * 界面偏好。
 *
 * 只放「跨页面、跨刷新需要记住、又不属于服务端数据」的东西：
 * 主题、侧边栏折叠、列表分页大小。
 * 列表的筛选条件不放这里 —— 它们属于 URL，见 `useListQueryState`。
 */
interface UiState {
  theme: ThemeMode
  sidebarCollapsed: boolean
  /** 表格每页行数。列表页共用，避免每页各记一个。 */
  pageSize: number
  setTheme: (t: ThemeMode) => void
  toggleSidebar: () => void
  setPageSize: (n: number) => void
}

export const useUiStore = create<UiState>()(
  persist(
    (set) => ({
      theme: 'light',
      sidebarCollapsed: false,
      pageSize: 20,
      setTheme: (theme) => set({ theme }),
      toggleSidebar: () => set((s) => ({ sidebarCollapsed: !s.sidebarCollapsed })),
      setPageSize: (pageSize) => set({ pageSize }),
    }),
    { name: 'mcp-hub.ui' },
  ),
)

/** 把 theme 落到 `<html class="dark">`，深色令牌挂在 .dark 上。 */
export function applyTheme(mode: ThemeMode) {
  const root = document.documentElement
  const prefersDark =
    typeof window !== 'undefined' && window.matchMedia('(prefers-color-scheme: dark)').matches
  const dark = mode === 'dark' || (mode === 'system' && prefersDark)
  root.classList.toggle('dark', dark)
  root.classList.toggle('light', !dark)
  root.style.colorScheme = dark ? 'dark' : 'light'
}

export function watchSystemTheme(getMode: () => ThemeMode) {
  const mq = window.matchMedia('(prefers-color-scheme: dark)')
  const onChange = () => applyTheme(getMode())
  mq.addEventListener('change', onChange)
  return () => mq.removeEventListener('change', onChange)
}
