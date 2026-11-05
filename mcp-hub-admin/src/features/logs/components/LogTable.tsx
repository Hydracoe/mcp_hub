import { ErrorKindBadge } from '@/components/common/StatusBadge'
import { Badge } from '@/components/ui/badge'
import { TableCell, TableHead, TableRow } from '@/components/ui/table'
import { TableFrame } from '@/components/common/TableFrame'
import type { TableFrameProps } from '@/components/common/TableFrame'
import { SortHead, type SortDir } from '@/components/common/SortHead'
import { Dash, MonoText } from '@/components/common/KeyValue'
import { formatDateTime, formatDuration } from '@/lib/format'
import { cn } from '@/lib/utils'
import type { LogItem, LogSort } from '@/lib/api/types'

interface Props {
  items: LogItem[]
  onRowClick: (item: LogItem) => void
  sort: LogSort
  dir: SortDir
  onSort: (col: LogSort) => void
  frame?: Omit<TableFrameProps, 'head' | 'colCount' | 'children'>
}

const COLS = [
  '时间',
  'Server',
  '工具',
  '调用方',
  '耗时',
  '状态',
  '错误分类',
  '错误',
  '下游密钥',
]

/** 列宽一律用百分比，总和恒为 100%——用定宽 px 会让表格撑出横向滚动条，
 *  且最后一列（无宽度、靠剩余空间分配）被前面的列吃成 0px。 */
const COL_W = [
  'w-[14%]', // 时间
  'w-[10%]', // Server
  'w-[13%]', // 工具
  'w-[10%]', // 调用方
  'w-[6%]', // 耗时
  'w-[9%]', // 状态
  'w-[11%]', // 错误分类
  'w-[15%]', // 错误
  'w-[12%]', // 下游密钥
]

/** 调用日志表。失败行整行染红边（`.is-error`）。只有时间 / 耗时 / 状态可排序。
 *  「调用方」是上游凭据，「下游密钥」是网关调下游的凭据，方向相反故分列。 */
export function LogTable({ items, onRowClick, sort, dir, onSort, frame }: Props) {
  // 全列左对齐：th 上的 text-align 工具类压不过 apple-ui.css 里未分层的
  // `.ui-table thead th`，表头右对齐、数字右对齐会造成整列视觉错位。
  const head = (label: string, col: LogSort, className?: string) => (
    <SortHead label={label} col={col} sort={sort} dir={dir} onSort={onSort} className={className} />
  )

  return (
    <TableFrame
      {...frame}
      colCount={COLS.length}
      tableClassName="table-fixed w-full"
      head={
        <TableRow>
          {head('时间', 'ts', COL_W[0])}
          <TableHead className={COL_W[1]}>Server</TableHead>
          <TableHead className={COL_W[2]}>工具</TableHead>
          <TableHead className={COL_W[3]}>调用方</TableHead>
          {head('耗时', 'durationMs', COL_W[4])}
          {head('状态', 'status', COL_W[5])}
          <TableHead className={COL_W[6]}>错误分类</TableHead>
          <TableHead className={COL_W[7]}>错误</TableHead>
          <TableHead className={COL_W[8]}>下游密钥</TableHead>
        </TableRow>
      }
    >
      {items.map((log) => {
        const durCls =
          log.errorKind === 'TIMEOUT'
            ? 'text-warning-strong'
            : log.status === 'error'
              ? 'text-error-strong'
              : ''
        return (
          <TableRow
            key={log.id}
            className={cn('cursor-pointer', log.status === 'error' && 'is-error')}
            onClick={() => onRowClick(log)}
            tabIndex={0}
            onKeyDown={(e) => {
              if (e.key === 'Enter' || e.key === ' ') {
                e.preventDefault()
                onRowClick(log)
              }
            }}
          >
            {/* 不加 whitespace-nowrap：久了会挤出格子压到 Server 列上，
                留默认折行，列宽不够时在日期与时间之间断开。 */}
            <TableCell className="font-mono" title={formatDateTime(log.ts)}>
              {formatDateTime(log.ts)}
            </TableCell>
            <TableCell className="truncate font-mono" title={log.serverKey}>
              {log.serverKey}
            </TableCell>
            <TableCell className="truncate font-mono" title={log.exposedName}>
              {log.exposedName}
            </TableCell>
            <TableCell className="max-w-45">
              <CallerCell log={log} />
            </TableCell>
            {/* tabular-nums：等宽数字让同列位数对齐，便于比较快慢 */}
            <TableCell className={`font-mono tabular-nums ${durCls}`}>
              {formatDuration(log.durationMs)}
            </TableCell>
            <TableCell>
              {log.status === 'ok' ? (
                <Badge variant="success" className="rounded-full! font-mono">
                  ok
                </Badge>
              ) : (
                <Badge variant="error" className="rounded-full! font-mono">
                  error
                </Badge>
              )}
            </TableCell>
            <TableCell>
              <ErrorKindBadge kind={log.errorKind} />
            </TableCell>
            <TableCell className="max-w-65">
              {log.error ? (
                <span className="block truncate font-mono text-error-strong" title={log.error}>
                  {log.error}
                </span>
              ) : (
                <Dash />
              )}
            </TableCell>
            {/* 显示密钥标签（后端按当前密钥表回填）；标签缺失（已删除）回退 id，
                悬停给出 id 便于对回密钥池里的行。 */}
            <TableCell>
              {log.credentialId ? (
                <span
                  className="block truncate text-foreground"
                  title={log.credentialLabel ? `${log.credentialLabel} · ${log.credentialId}` : log.credentialId}
                >
                  {log.credentialLabel ?? log.credentialId}
                </span>
              ) : (
                <span className="text-muted-foreground" title="该 server 未启用密钥池（单密钥模式）">
                  —
                </span>
              )}
            </TableCell>
          </TableRow>
        )
      })}
    </TableFrame>
  )
}

/** 调用方单元格。三种取值：有名字快照、只有 id、两者都空（匿名）。
 *  名字是快照，改密钥名或删密钥都不影响历史行。 */
function CallerCell({ log }: { log: LogItem }) {
  if (log.apiKeyName) {
    return (
      <span
        className="block truncate text-foreground"
        title={`${log.apiKeyName}${log.apiKeyId ? ` · ${log.apiKeyId}` : ''}`}
      >
        {log.apiKeyName}
      </span>
    )
  }
  if (log.apiKeyId) {
    return <MonoText title={log.apiKeyId}>{log.apiKeyId}</MonoText>
  }
  return (
    <span className="text-muted-foreground" title="调用发生时端点未启用接入鉴权">
      匿名
    </span>
  )
}
