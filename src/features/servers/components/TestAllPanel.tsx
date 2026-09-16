import { useQuery } from '@tanstack/react-query'
import { useState } from 'react'
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'
import { Icon } from '@/components/common/Icon'
import { Badge } from '@/components/ui/badge'
import { ErrorKindBadge } from '@/components/common/StatusBadge'
import { formatDuration, formatRelative } from '@/lib/format'
import type { TestAllResponse } from '@/lib/api/types'

/**
 * 最近一次全量测试结果面板。
 * 缺口 B4：后端不保存报告，useTestAllServers 把结果塞进
 * queryKey ['servers','test-all'] 的内存里，这里 enabled:false 读回来。
 * ⚠ 设计稿原版没有这个面板（全量测试只有按钮没有结果呈现），此处补齐。
 */
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
        <ul className="max-h-80 divide-y divide-separator overflow-y-auto">
          {results.map((r) => (
            <li key={r.serverId} className="flex flex-wrap items-center gap-x-3 gap-y-1 px-4 py-2 text-[12px]">
              <span className="font-mono text-foreground" title={r.serverKey}>{r.serverKey}</span>
              {r.ok ? (
                <Badge variant="success">ok</Badge>
              ) : r.enabled ? (
                <Badge variant="error">failed</Badge>
              ) : (
                <Badge variant="neutral">停用</Badge>
              )}
              {typeof r.latencyMs === 'number' ? (
                <span className="font-mono tabular-nums text-muted-foreground">{r.latencyMs}ms</span>
              ) : null}
              <span className="font-mono text-[11px] text-muted-foreground">{r.protocolVersion ?? '—'}</span>
              {typeof r.toolCount === 'number' ? (
                <span className="font-mono tabular-nums text-muted-foreground">{r.toolCount} 工具</span>
              ) : null}
              {r.errorKind ? <ErrorKindBadge kind={r.errorKind} /> : null}
              {!r.ok && r.hint ? (
                <span className="basis-full text-[11.5px] text-warning-strong">{r.hint}</span>
              ) : null}
              {!r.ok && r.error ? (
                <span className="basis-full font-mono text-[11px] text-muted-foreground">{r.error}</span>
              ) : null}
            </li>
          ))}
          {results.length === 0 ? (
            <li className="px-4 py-6 text-center text-[12px] text-muted-foreground">
              共 {total} 个 Server，全部处于停用状态
            </li>
          ) : null}
        </ul>
      </CollapsibleContent>
    </Collapsible>
  )
}
