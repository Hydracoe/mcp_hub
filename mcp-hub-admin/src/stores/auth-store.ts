import { create } from 'zustand'

import { apiKeyStorage } from '@/lib/api/client'

/** 管理接口鉴权状态。后端未启用 `--api-key` 时无感：请求放行，解锁框只在 401 时出现。
 *  令牌只存 localStorage 的 `mcp-hub.admin.apiKey`；不放进 zustand persist，避免两处副本清不干净或互相覆盖。 */
interface AuthState {
  /** 当前令牌。空串 = 未启用鉴权。 */
  apiKey: string
  /** 上一次因 401 被拒绝。为 true 时 AuthGate 强制弹出解锁框。 */
  rejected: boolean
  unlock: (key: string) => void
  markRejected: () => void
  clear: () => void
}

export const useAuthStore = create<AuthState>()((set) => {
  // 存量用户：旧版令牌存在 'mcp-hub.auth'，迁移到新键，否则会被要求重新输入。
  apiKeyStorage.migrateLegacy()

  return {
    apiKey: apiKeyStorage.read(),
    rejected: false,
    unlock: (key) => {
      apiKeyStorage.write(key)
      set({ apiKey: key, rejected: false })
    },
    markRejected: () => set({ rejected: true }),
    clear: () => {
      apiKeyStorage.write('')
      set({ apiKey: '', rejected: false })
    },
  }
})
