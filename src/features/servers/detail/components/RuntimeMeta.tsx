import { KeyValueGrid } from '@/components/common/KeyValue'
import { DrainingBadge, ServerStatusBadge } from '@/components/common/StatusBadge'
import { ErrorKindBadge } from '@/components/common/StatusBadge'
import { formatDateTime, formatDuration, formatRelative } from '@/lib/format'
import type { ServerDetail } from '@/lib/api/types'

export interface RuntimeMetaProps {
  server: ServerDetail
}

export function RuntimeMeta({ server }: RuntimeMetaProps) {
  const items = [
    { key: 'created', label: '创建时间', value: formatDateTime(server.createdAt) },
    { key: 'updated', label: '更新时间', value: formatDateTime(server.updatedAt) },
    {
      key: 'lastCheck',
      label: '最近探活',
      value: server.lastCheckAt ? formatRelative(server.lastCheckAt) : '尚未探活',
    },
    {
      key: 'latency',
      label: '最近延迟',
      value: server.lastLatencyMs != null ? formatDuration(server.lastLatencyMs) : '—',
    },
    { key: 'status', label: '健康状态', value: <ServerStatusBadge status={server.status} /> },
    {
      key: 'tools',
      label: '工具过滤',
      value: (
        <span className="font-mono">
          {server.exposedToolCount}
          <span className="text-muted-foreground"> 暴露 / </span>
          {server.toolCount}
          <span className="text-muted-foreground"> 声明</span>
        </span>
      ),
    },
  ]

  return (
    <div className="form-card">
      <div className="form-section-header">运行时元数据</div>
      <div className="form-section-body">
        <KeyValueGrid items={items} columns={2} />
        {(server.draining || server.lastErrorKind) && (
          <div className="mt-3 flex flex-wrap items-center gap-2">
            {server.draining ? <DrainingBadge inflight={server.drainingInflight} /> : null}
            {server.lastErrorKind ? <ErrorKindBadge kind={server.lastErrorKind} /> : null}
            {server.lastError ? (
              <span className="font-mono text-[11.5px] text-error-strong">{server.lastError}</span>
            ) : null}
          </div>
        )}
        {server.lastErrorHint ? (
          <p className="mt-2 text-[11.5px] text-muted-foreground">{server.lastErrorHint}</p>
        ) : null}
      </div>
    </div>
  )
}
