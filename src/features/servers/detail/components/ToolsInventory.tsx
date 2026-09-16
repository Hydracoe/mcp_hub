import { useMemo } from 'react'

import { Icon } from '@/components/common/Icon'
import { Badge } from '@/components/ui/badge'
import {
  BareTable,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
  TableSkeletonRows,
  TableStateRow,
} from '@/components/ui/table'
import { useRefreshTools, useTools } from '@/lib/api/queries'
import { formatInt } from '@/lib/format'
import type { ServerDetail } from '@/lib/api/types'

export interface ToolsInventoryProps {
  server: ServerDetail
}

export function ToolsInventory({ server }: ToolsInventoryProps) {
  const { data, isLoading } = useTools({ serverKey: server.key })
  const refresh = useRefreshTools()

  const downstream = server.tools ?? []
  const exposedNames = useMemo(
    () => new Set((data?.items ?? []).map((t) => t.name)),
    [data],
  )
  const exposedCount = server.exposedToolCount ?? data?.items?.length ?? 0
  const declared = downstream.length
  const blocked = Math.max(0, declared - exposedCount)

  /* 下游声明了工具清单就按下游的来，否则退回聚合结果 —— 未探活时前者是空的。 */
  const names = declared > 0 ? downstream : (data?.items ?? []).map((t) => t.name)

  return (
    <div className="form-card">
      <div className="form-section-header flex items-center justify-between">
        <span>工具清单</span>
        <div className="flex items-center gap-2">
          <button
            type="button"
            className="ui-btn ui-btn-ghost h-7 px-2.5 text-[12px]"
            onClick={() => refresh.mutate(server.id)}
            disabled={refresh.isPending}
          >
            {refresh.isPending ? <Icon name="refresh-cw" size={13} className="animate-spin" /> : <Icon name="refresh-cw" size={13} />}
            刷新工具
          </button>
        </div>
      </div>
      <div className="form-section-body">
        <div className="mb-3 flex flex-wrap gap-2">
          <Stat label="下游声明" value={declared} />
          <Stat label="过滤后暴露" value={exposedCount} tone="success" />
          <Stat label="被挡掉" value={blocked} tone={blocked > 0 ? 'warning' : 'neutral'} />
        </div>

        {/* 这里已经身处 form-card 里，再包一层 .ui-table-wrap 会出现嵌套卡片；
            用 BareTable，横向滚动交给 .ui-table-scroll。 */}
        <div className="ui-table-scroll">
          <BareTable className="min-w-[560px]">
            <TableHeader>
              <TableRow>
                <TableHead>原始工具名</TableHead>
                <TableHead>暴露名</TableHead>
                <TableHead className="text-right">状态</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {isLoading ? (
                <TableSkeletonRows rows={5} cols={3} />
              ) : names.length === 0 ? (
                <TableStateRow colSpan={3}>
                  <div className="px-6 py-8 text-center text-muted-foreground">
                    暂无工具（可能尚未探活或下游未返回工具清单）
                  </div>
                </TableStateRow>
              ) : (
                names.map((name) => {
                  const exposed = exposedNames.has(name)
                  const entry = (data?.items ?? []).find((t) => t.name === name)
                  return (
                    <TableRow key={name}>
                      <TableCell className="font-mono">{name}</TableCell>
                      <TableCell className="font-mono text-muted-foreground">
                        {entry?.exposedName ?? (exposed ? `${server.key}__${name}` : '—')}
                      </TableCell>
                      <TableCell className="text-right">
                        {exposed ? (
                          <Badge variant={entry?.available ? 'success' : 'error'}>
                            {entry?.available ? '暴露' : '不可用'}
                          </Badge>
                        ) : (
                          <Badge variant="neutral">被挡掉</Badge>
                        )}
                      </TableCell>
                    </TableRow>
                  )
                })
              )}
            </TableBody>
          </BareTable>
        </div>
        <p className="form-hint mt-2">
          <Icon name="funnel" size={13} className="mr-1 inline-block align-[-2px]" />
          下游声明 {formatInt(declared)} 个、过滤后暴露 {formatInt(exposedCount)} 个；
          被挡掉的是未进入聚合池的工具，可在「工具过滤」中调整白/黑名单。
        </p>
      </div>
    </div>
  )
}

function Stat({
  label,
  value,
  tone = 'neutral',
}: {
  label: string
  value: number
  tone?: 'success' | 'warning' | 'neutral'
}) {
  const toneClass = {
    neutral: 'text-foreground',
    success: 'text-success-strong',
    warning: 'text-warning-strong',
  }[tone]
  return (
    <span className="inline-flex items-center gap-1.5 rounded-md bg-fill-tertiary px-2.5 py-1 text-[12px]">
      <span className="text-icon-muted">{label}</span>
      <span className={`font-mono font-semibold tabular-nums ${toneClass}`}>{formatInt(value)}</span>
    </span>
  )
}
