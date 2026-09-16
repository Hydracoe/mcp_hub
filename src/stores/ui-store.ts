import { create } from 'zustand'
import { persist } from 'zustand/middleware'

/** 界面偏好：侧边栏折叠、列表分页大小。
 *  筛选条件不放这里，它们属于 URL（见 `useListQueryState`）。 */
interface UiState {
  sidebarCollapsed: boolean
  /** 表格每页行数。列表页共用，避免每页各记一个。 */
  pageSize: number
  toggleSidebar: () => void
  setPageSize: (n: number) => void
}

export const useUiStore = create<UiState>()(
  persist(
    (set) => ({
      sidebarCollapsed: false,
      pageSize: 20,
      toggleSidebar: () => set((s) => ({ sidebarCollapsed: !s.sidebarCollapsed })),
      setPageSize: (pageSize) => set({ pageSize }),
    }),
    { name: 'mcp-hub.ui' },
  ),
)
