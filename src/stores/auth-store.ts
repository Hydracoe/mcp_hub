import { create } from 'zustand'
import { persist } from 'zustand/middleware'

import { apiKeyStorage } from '@/lib/api/client'

/** 管理接口鉴权状态。后端未启用 `--api-key` 时完全无感：
 *  请求照常放行，解锁弹窗只在收到 401 时才出现。 */
interface AuthState {
  /** 当前令牌。空串 = 未启用鉴权。 */
  apiKey: string
  /** 上一次因 401 被拒绝。为 true 时 AuthGate 强制弹出解锁框。 */
  rejected: boolean
  unlock: (key: string) => void
  markRejected: () => void
  clear: () => void
}

export const useAuthStore = create<AuthState>()(
  persist(
    (set) => ({
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
    }),
    {
      name: 'mcp-hub.auth',
      // 只持久化令牌；`rejected` 是本次会话的瞬时状态。
      partialize: (s) => ({ apiKey: s.apiKey }),
    },
  ),
)
