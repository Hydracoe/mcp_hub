import { Badge } from '@/components/ui/badge'
import { KeyValueGrid } from '@/components/common/KeyValue'
import { formatDateTime, formatInt, formatUptime } from '@/lib/format'
import type { OverviewResponse } from '@/lib/api/types'

/**
 * 网关总览：运行时信息。
 * 统一换成语义 token（ui-card / code-chip）。
 */
export function RuntimeOverview({ overview }: { overview: OverviewResponse }) {
  const { servers, uptimeSeconds, builtAt, ready, endpoints } = overview
  const entries = Object.entries(endpoints ?? {})

  return (
    <div className="ui-card p-5">
      <div className="mb-4 text-[11px] font-semibold tracking-[0.06em] text-icon-muted uppercase">
        运行时
      </div>
      <div className="grid gap-6 lg:grid-cols-[minmax(0,1fr)_minmax(260px,360px)] lg:gap-8">
        <KeyValueGrid
          columns={4}
          items={[
            { key: 'uptime', label: '运行时长', value: formatUptime(uptimeSeconds) },
            { key: 'built', label: '构建时间', value: formatDateTime(builtAt) },
            {
              key: 'ready',
              label: '就绪',
              value: ready ? <Badge variant="success">Ready</Badge> : <Badge variant="neutral">未就绪</Badge>,
            },
            { key: 'servers', label: 'Server 数', value: formatInt(servers.total) },
          ]}
        />
        <div className="lg:border-l lg:border-separator lg:pl-8">
          <div className="mb-1.5 text-[10.5px] font-semibold tracking-[0.06em] text-icon-muted uppercase">
            端点
          </div>
          <div className="flex flex-wrap gap-2">
            {entries.length ? (
              entries.map(([k, v]) => (
                <code key={k} className="code-chip" title={k}>
                  {v}
                </code>
              ))
            ) : (
              <span className="text-[12px] text-muted-foreground">—</span>
            )}
          </div>
        </div>
      </div>
    </div>
  )
}
