import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'

import { App } from './App'
import './styles/index.css'

/** Mock 开关：默认开。设 `VITE_USE_MOCK=false` 走真实后端（Vite 代理 /api → :9090）。 */
const useMock = import.meta.env.VITE_USE_MOCK !== 'false'

async function bootstrap() {
  if (useMock) {
    // 必须在首屏请求之前启动 worker，否则第一批 /api 请求会真的打到
    // dev server 上并拿到 404 —— 表现为「首屏永远空着，刷新一下才好」。
    const { startMockWorker } = await import('./lib/mock/browser')
    await startMockWorker()
  }

  const rootEl = document.getElementById('root')
  if (!rootEl) throw new Error('#root 不存在')

  createRoot(rootEl).render(
    <StrictMode>
      <App />
    </StrictMode>,
  )
}

void bootstrap()
