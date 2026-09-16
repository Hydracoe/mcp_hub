import { setupWorker } from 'msw/browser'
import { handlers } from './handlers'

/**
 * 启动 MSW 浏览器 worker。
 *
 * `onUnhandledRequest: 'bypass'`：只拦截 `/api/*`，把 Vite 的资源请求
 * （/@vite/client、/src/*、HMR websocket 等）放行，否则页面会白屏。
 */
export async function startMockWorker(): Promise<void> {
  const worker = setupWorker(...handlers)
  await worker.start({
    onUnhandledRequest: 'bypass',
    quiet: false,
    serviceWorker: { url: '/mockServiceWorker.js' },
  })
}
