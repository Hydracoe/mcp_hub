import { create } from 'zustand'

import { apiKeyStorage } from '@/lib/api/client'

/** 管理接口鉴权状态。后端未启用 `--api-key` 时完全无感：
 *  请求照常放行，解锁弹窗只在收到 401 时才出现。
 *
 *  令牌只有一个存放处：localStorage 的 `mcp-hub.admin.apiKey`（见 apiKeyStorage）。
 *  这里不再用 zustand persist 存它——同一份令牌存两处，登出清不干净、
 *  重载时两边还会互相覆盖。 */
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
  // 存量用户：老版本把令牌也存在 persist 的 'mcp-hub.auth' 键里，
  // 迁移一次到新键，否则老用户会看到"令牌丢了"、被要求重新输入。
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
