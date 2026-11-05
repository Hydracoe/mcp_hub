import { useQuery } from '@tanstack/react-query'
import { useState } from 'react'
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'
import {
  BareTable,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
  TableStateRow,
} from '@/components/ui/table'
import { Icon } from '@/components/common/Icon'
import { Badge } from '@/components/ui/badge'
import { ErrorKindBadge } from '@/components/common/StatusBadge'
import { formatDuration, formatRelative } from '@/lib/format'
import { cn } from '@/lib/utils'
import type { ProbeResultItem, TestAllResponse } from '@/lib/api/types'

/** 最近一次全量测试结果面板。后端不保存报告，`useTestAllServers` 把结果放在
 *  queryKey ['servers','test-all'] 的内存里，这里 enabled:false 读回来。
 *
 *  结果走 BareTable（卡片内已有边框，不再包 ui-table-wrap 外壳）：
 *  7 列定宽对齐，失败行用 `.is-error` 红边突出——与 LogTable 同一语言。 */

/* 列宽一律百分比、总和 100%，与 ServersTable 同一套约定。 */
const COL_W = [
  'w-[22%]', // Server
  'w-[9%]', // 结果
  'w-[9%]', // 延迟
  'w-[12%]', // 协议
  'w-[8%]', // 工具
  'w-[13%]', // 分类
  'w-[27%]', // 详情
]

export function TestAllPanel() {
  const { data } = useQuery<TestAllResponse>({ queryKey: ['servers', 'test-all'], enabled: false })
  const [open, setOpen] = useState(true)

  if (!data) return null
  const { total, healthy, failed, disabled, tookMs, testedAt, results } = data

  return (
    <Collapsible open={open} onOpenChange={setOpen} className="ui-card overflow-hidden">
      <CollapsibleTrigger className="flex w-full items-center gap-2 px-4 py-3 text-left">
        <Icon name="zap" size={15} className="text-icon-muted" />
        <span className="text-[13px] font-semibold text-foreground">最近一次全量测试结果</span>
        <span className="flex items-center gap-1.5 text-[11.5px] text-muted-foreground">
          <Badge variant="success">{healthy} 健康</Badge>
          <Badge variant="error">{failed} 失败</Badge>
          <Badge variant="neutral">{disabled} 停用(不计成败)</Badge>
          <span className="font-mono tabular-nums">{formatDuration(tookMs)}</span>
          <span className="font-mono tabular-nums">{formatRelative(testedAt)}</span>
        </span>
        <Icon name={open ? 'chevron-up' : 'chevron-down'} size={15} className="ml-auto text-icon-muted" />
      </CollapsibleTrigger>
      <CollapsibleContent className="border-t border-separator">
        <div className="max-h-80 overflow-y-auto">
          <BareTable className="table-fixed w-full">
            <TableHeader className="sticky top-0 z-10">
              <TableRow>
                <TableHead className={COL_W[0]}>Server</TableHead>
                <TableHead className={COL_W[1]}>结果</TableHead>
                <TableHead className={COL_W[2]}>延迟</TableHead>
                <TableHead className={COL_W[3]}>协议</TableHead>
                <TableHead className={COL_W[4]}>工具</TableHead>
                <TableHead className={COL_W[5]}>分类</TableHead>
                <TableHead className={COL_W[6]}>详情</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {results.map((r) => (
                <ResultRow key={r.serverId} r={r} />
              ))}
              {results.length === 0 ? (
                <TableStateRow colSpan={COL_W.length}>
                  <div className="py-6 text-center text-[12px] text-muted-foreground">
                    共 {total} 个 Server，全部处于停用状态
                  </div>
                </TableStateRow>
              ) : null}
            </TableBody>
          </BareTable>
        </div>
      </CollapsibleContent>
    </Collapsible>
  )
}

function ResultRow({ r }: { r: ProbeResultItem }) {
  /* 停用项不算失败：ok=false 只在「启用且探活失败」时染红边。 */
  const failed = !r.ok && r.enabled
  return (
    <TableRow className={cn(failed && 'is-error')}>
      <TableCell>
        <div className="truncate font-medium text-foreground" title={r.name}>
          {r.name}
        </div>
        <div className="truncate font-mono cell-sub text-muted-foreground" title={r.serverKey}>
          {r.serverKey}
        </div>
      </TableCell>

      <TableCell>
        {r.ok ? (
          <Badge variant="success">ok</Badge>
        ) : failed ? (
          <Badge variant="error">failed</Badge>
        ) : (
          <Badge variant="neutral">停用</Badge>
        )}
      </TableCell>

      <TableCell>
        {typeof r.latencyMs === 'number' ? (
          <span className="font-mono tabular-nums">{r.latencyMs}ms</span>
        ) : (
          <span className="text-muted-foreground">—</span>
        )}
      </TableCell>

      <TableCell>
        {r.protocolVersion ? (
          <span className="font-mono text-muted-foreground">{r.protocolVersion}</span>
        ) : (
          <span className="text-muted-foreground">—</span>
        )}
      </TableCell>

      <TableCell>
        {typeof r.toolCount === 'number' ? (
          <span className="font-mono tabular-nums">{r.toolCount}</span>
        ) : (
          <span className="text-muted-foreground">—</span>
        )}
      </TableCell>

      <TableCell>
        {r.errorKind ? <ErrorKindBadge kind={r.errorKind} /> : <span className="text-muted-foreground">—</span>}
      </TableCell>

      <TableCell>
        {!r.ok ? (
          <div className="space-y-0.5">
            {r.hint ? (
              <div className="truncate text-[11.5px] text-warning-strong" title={r.hint}>
                {r.hint}
              </div>
            ) : null}
            {r.error ? (
              <div className="truncate font-mono text-[11px] text-muted-foreground" title={r.error}>
                {r.error}
              </div>
            ) : null}
          </div>
        ) : null}
      </TableCell>
    </TableRow>
  )
}
