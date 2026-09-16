import { useState } from 'react'

import { getConnect } from '@/lib/api/endpoints'
import { Button } from '@/components/ui/button'
import { Icon } from '@/components/common/Icon'
import { Spinner } from '@/components/common/States'

interface Props {
  mcpEndpoint: string
}

interface TestResult {
  ok: boolean
  ms: number
  error?: string
}

export function ConnectTest({ mcpEndpoint }: Props) {
  const base = mcpEndpoint.replace(/\/mcp$/, '') || mcpEndpoint
  const [url, setUrl] = useState(base)
  const [running, setRunning] = useState(false)
  const [result, setResult] = useState<TestResult | null>(null)

  const run = async () => {
    setRunning(true)
    const t0 = performance.now()
    try {
      // ⚠ 设计稿原版是 mockJS（直接写死「连接成功 · 延迟 12ms」）。
      // 这里改成真实调用网关的接入接口，展示真实往返耗时与失败原因。
      await getConnect()
      setResult({ ok: true, ms: Math.round(performance.now() - t0) })
    } catch (e) {
      setResult({
        ok: false,
        ms: Math.round(performance.now() - t0),
        error: e instanceof Error ? e.message : String(e),
      })
    } finally {
      setRunning(false)
    }
  }

  return (
    <div>
      <div className="flex flex-col gap-3 sm:flex-row sm:items-center">
        <input
          type="text"
          value={url}
          onChange={(e) => setUrl(e.target.value)}
          aria-label="网关地址"
          className="form-input w-full flex-1 px-3 py-2 font-mono text-sm"
        />
        <Button onClick={run} disabled={running} className="shrink-0">
          {running ? <Spinner size={14} /> : <Icon name="plug" size={14} />}
          {running ? '测试中…' : '测试连接'}
        </Button>
      </div>

      <div
        className={
          'mt-3 rounded-md border px-3 py-2 text-sm ' +
          (result?.ok
            ? 'border-success/30 bg-success-surface text-success-strong'
            : result
              ? 'border-error/30 bg-error-surface text-error-strong'
              : 'border-separator bg-fill-tertiary text-muted-foreground')
        }
      >
        {!result ? (
          '点击测试按钮检查网关连通性'
        ) : result.ok ? (
          <span className="inline-flex items-center gap-1.5">
            <Icon name="circle-check" size={14} />
            连接成功 · 往返延迟 {result.ms}ms
          </span>
        ) : (
          <span className="inline-flex items-center gap-1.5">
            <Icon name="triangle-alert" size={14} />
            连接失败 · {result.ms}ms · {result.error}
          </span>
        )}
      </div>
    </div>
  )
}
