import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { RouterProvider } from 'react-router-dom'

import { AuthGate } from '@/features/auth/AuthGate'
import { Toaster } from '@/components/ui/sonner'
import { TooltipProvider } from '@/components/ui/tooltip'
import { router } from './router'
import { ApiError } from '@/lib/api/client'

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      // 管理后台的读接口便宜且数据在变，重新聚焦就刷新是合理的；
      // 4xx 不重试 —— 参数错了重试一百次还是错。
      refetchOnWindowFocus: true,
      retry: (failureCount, error) => {
        if (error instanceof ApiError && error.status >= 400 && error.status < 500) return false
        return failureCount < 2
      },
      staleTime: 3_000,
    },
  },
})

export function App() {
  return (
    <QueryClientProvider client={queryClient}>
      <TooltipProvider delayDuration={300}>
        <AuthGate>
          <RouterProvider router={router} />
        </AuthGate>
        <Toaster />
      </TooltipProvider>
    </QueryClientProvider>
  )
}
