import { useKeyEvents } from '@/lib/api/queries'
import { KeyEventBadge } from '@/components/common/StatusBadge'
import { TableCell, TableHead, TableRow } from '@/components/ui/table'
import { TableFrame } from '@/components/common/TableFrame'
import type { TableFrameProps } from '@/components/common/TableFrame'
import { EmptyState } from '@/components/common/States'
import { MonoText } from '@/components/common/KeyValue'
import { formatDateTime } from '@/lib/format'

interface Props {
  serverId?: string
  /** 与调用日志共用「实时/暂停」开关：暂停时连密钥事件一起停，否则开关看起来是坏的。 */
  enabled?: boolean
  /** 外壳、状态位与页脚统一交给 TableFrame。 */
  frame?: Omit<TableFrameProps, 'head' | 'colCount' | 'children'>
}

const COLS = ['时间', 'Server', '事件类型', '密钥', '详情']

export function KeyEventsTable({ serverId, enabled = true, frame }: Props) {
  const { data, isLoading, isError } = useKeyEvents(serverId, enabled)

  const items = data?.items ?? []

  /* 后端可能还没有这个端点（404）。这是「接口没上」不是「加载失败」，
     所以安静降级成空态，不弹 toast、不打崩整页。 */
  const empty = isError ? (
    <EmptyState
      icon="shield-alert"
      title="该视图暂未开放"
      description="密钥事件接口尚未在后端启用，暂时无法展示实时密钥调度事件。"
    />
  ) : (
    <EmptyState
      icon="key-round"
      title="暂无密钥事件"
      description="冷却、弃用、恢复等密钥调度事件会显示在这里。"
    />
  )

  return (
    <TableFrame
      {...frame}
      colCount={COLS.length}
      tableClassName="min-w-[760px]"
      loading={isLoading}
      skeletonRows={5}
      isEmpty={isError || items.length === 0}
      empty={empty}
      head={
        <TableRow>
          <TableHead>时间</TableHead>
          <TableHead>Server</TableHead>
          <TableHead>事件类型</TableHead>
          <TableHead>密钥</TableHead>
          <TableHead>详情</TableHead>
        </TableRow>
      }
    >
      {items.map((ev) => (
        <TableRow key={ev.id}>
          <TableCell className="whitespace-nowrap font-mono">
            {formatDateTime(ev.ts)}
          </TableCell>
          <TableCell className="font-mono" title={ev.serverKey}>
            {ev.serverKey}
          </TableCell>
          <TableCell>
            <KeyEventBadge type={ev.type} />
          </TableCell>
          <TableCell>
            <MonoText title={ev.credentialId}>{ev.credentialId}</MonoText>
            {ev.credentialLabel ? (
              <div className="cell-sub text-muted-foreground">{ev.credentialLabel}</div>
            ) : null}
          </TableCell>
          <TableCell className="max-w-[320px] font-mono text-muted-foreground" title={ev.detail}>
            {ev.detail ?? '—'}
          </TableCell>
        </TableRow>
      ))}
    </TableFrame>
  )
}
