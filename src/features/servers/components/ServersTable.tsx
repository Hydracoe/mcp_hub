import { Link } from 'react-router-dom'
import { TableCell, TableHead, TableRow } from '@/components/ui/table'
import { TableFrame } from '@/components/common/TableFrame'
import type { TableFrameProps } from '@/components/common/TableFrame'
import { SortHead, type SortDir } from '@/components/common/SortHead'
import { Switch } from '@/components/ui/switch'
import { Button } from '@/components/ui/button'
import { Icon } from '@/components/common/Icon'
import {
  DrainingBadge,
  ServerStatusBadge,
  StoppedBadge,
  TransportTag,
} from '@/components/common/StatusBadge'
import { formatPercent, formatRelative } from '@/lib/format'
import { cn } from '@/lib/utils'
import type { ServerSummary } from '@/lib/api/types'

/** 7 列，宽表。table-fixed + 每列定宽：auto 布局的列宽由「当页最宽的行」
    决定，排序/翻页换一批行，列宽就跟着跳 —— 定宽后行集合怎么换都不动。 */
const COLS = 7

/** 各列定宽（px）。总和 ≈1256，窄容器由外层 wrap 横向滚动，宽屏按比例拉伸。 */
const COL_W = ['w-[260px]', 'w-28', 'w-[170px]', 'w-[180px]', 'w-[170px]', 'w-36', 'w-[260px]']

interface Props {
  rows: ServerSummary[]
  /** 外壳、状态位与页脚统一交给 TableFrame，页面不再自己包壳。 */
  frame?: Omit<TableFrameProps, 'head' | 'colCount' | 'children'>
  sort: string
  dir: 1 | -1
  onSort: (key: string) => void
  onToggleEnabled: (s: ServerSummary, enabled: boolean) => void
  togglingId: string | null
  onTest: (s: ServerSummary) => void
  testingId: string | null
  onOpenTools: (s: ServerSummary) => void
  onOpenCreds: (s: ServerSummary) => void
  onDelete: (s: ServerSummary) => void
}

const SORTABLE = new Set(['name', 'status', 'tools', 'keys', 'latency'])

export function ServersTable(props: Props) {
  const { rows, sort, dir, onSort, frame } = props
  /* 共用表头用 'asc' | 'desc'，列表页的 URL 状态用 1 | -1（`useListQueryState`）。
     换算只在这一处做 —— 两套表示各自服务自己的调用方，谁也别改谁。 */
  const sortDir: SortDir = dir === 1 ? 'asc' : 'desc'
  const head = (label: string, col: string, i: number, className?: string) =>
    SORTABLE.has(col) ? (
      <SortHead label={label} col={col} sort={sort} dir={sortDir} onSort={onSort} className={cn(COL_W[i], className)} />
    ) : (
      <TableHead className={cn(COL_W[i], className)}>{label}</TableHead>
    )

  return (
    <TableFrame
      {...frame}
      colCount={COLS}
      tableClassName="table-fixed w-full"
      head={
        <TableRow>
          {head('名称 / Key', 'name', 0)}
          <TableHead className={COL_W[1]}>传输</TableHead>
          {head('状态', 'status', 2)}
          {head('工具', 'tools', 3)}
          {head('密钥', 'keys', 4)}
          {head('延迟 / 检查', 'latency', 5)}
          <TableHead className={cn(COL_W[6], 'text-right')}>操作</TableHead>
        </TableRow>
      }
    >
      {rows.map((s) => (
        <ServerRow key={s.id} s={s} {...props} />
      ))}
    </TableFrame>
  )
}

function ServerRow({ s, onToggleEnabled, togglingId, onTest, testingId, onOpenTools, onOpenCreds, onDelete }: Props & { s: ServerSummary }) {
  const disabled = !s.enabled
  return (
    <TableRow className={disabled ? 'opacity-60' : undefined}>
      {/* 名称 / Key（定宽列，超长自截断，title 兜全文） */}
      <TableCell>
        <Link
          to={`/servers/${s.id}`}
          className="block truncate font-medium text-foreground hover:underline"
          title={s.name}
        >
          {s.name}
        </Link>
        <div className="truncate font-mono cell-sub text-muted-foreground" title={s.key}>
          {s.key}
        </div>
      </TableCell>

      {/* 传输 */}
      <TableCell>
        <TransportTag transport={s.transport} />
      </TableCell>

      {/* 状态 */}
      <TableCell>
        <div className="flex flex-col gap-1">
          {disabled ? <StoppedBadge /> : <ServerStatusBadge status={s.status} />}
          {s.draining ? <DrainingBadge inflight={s.drainingInflight} /> : null}
        </div>
      </TableCell>

      {/* 工具子列 */}
      <TableCell>
        <button type="button" onClick={() => onOpenTools(s)} className="text-left hover:opacity-80" title={toolTitle(s)}>
          <span className="font-mono tabular-nums text-foreground">{toolSummary(s)}</span>
          {s.toolFilterMode !== 'allow_all' ? <span className="ml-1 cell-sub text-warning-strong">· 过滤</span> : null}
        </button>
      </TableCell>

      {/* 密钥子列 */}
      <TableCell>
        {s.keyPool ? (
          <button type="button" onClick={() => onOpenCreds(s)} className="text-left hover:opacity-80" title={keyTitle(s)}>
            <span className="font-mono tabular-nums text-foreground">
              在用 {s.usableKeyCount} / {s.keyCount}
            </span>
            {typeof s.keySuccessRate === 'number' ? (
              <span className="ml-1 cell-sub text-muted-foreground">{formatPercent(s.keySuccessRate)}</span>
            ) : null}
          </button>
        ) : (
          <span className="text-muted-foreground">无需密钥</span>
        )}
      </TableCell>

      {/* 延迟 / 检查 */}
      <TableCell>
        {typeof s.lastLatencyMs === 'number' ? (
          <div>
            <span className="font-mono tabular-nums text-foreground">{s.lastLatencyMs}ms</span>
            {s.lastCheckAt ? <div className="font-mono cell-sub text-muted-foreground">{formatRelative(s.lastCheckAt)}</div> : null}
          </div>
        ) : (
          <span className="text-muted-foreground">尚未探活</span>
        )}
      </TableCell>

      {/* 操作 */}
      <TableCell>
        <div className="flex items-center justify-end gap-1.5">
          <Switch
            checked={s.enabled}
            onCheckedChange={(v) => onToggleEnabled(s, v)}
            disabled={togglingId === s.id}
            aria-label={s.enabled ? `停用 ${s.name}` : `启用 ${s.name}`}
          />
          <Button
            variant="ghost"
            size="icon-sm"
            aria-label={`测试 ${s.name}`}
            disabled={testingId === s.id}
            onClick={() => onTest(s)}
          >
            {testingId === s.id ? <Icon name="refresh-cw" size={14} className="animate-spin" /> : <Icon name="zap" size={14} />}
          </Button>
          <Button variant="outline" size="xs" onClick={() => onOpenTools(s)}>
            <Icon name="wrench" size={12} /> 工具
          </Button>
          <Button variant="outline" size="xs" onClick={() => onOpenCreds(s)}>
            <Icon name="key-round" size={12} /> 密钥
          </Button>
          <Button
            variant="ghost"
            size="icon-sm"
            aria-label={`删除 ${s.name}`}
            className="text-error-strong hover:bg-error/10"
            onClick={() => onDelete(s)}
          >
            <Icon name="trash-2" size={14} />
          </Button>
        </div>
      </TableCell>
    </TableRow>
  )
}

function toolSummary(s: ServerSummary): string {
  return s.enabledToolCount != null ? `启用 ${s.enabledToolCount} / 共 ${s.toolCount}` : `共 ${s.toolCount}`
}

function toolTitle(s: ServerSummary): string {
  const parts = [toolSummary(s)]
  if (typeof s.toolSuccessRate === 'number') parts.push(`成功率 ${formatPercent(s.toolSuccessRate)}`)
  if (s.toolFilterMode !== 'allow_all') parts.push(`过滤模式 ${s.toolFilterMode}`)
  return parts.join(' · ')
}

function keyTitle(s: ServerSummary): string {
  if (!s.keyPool) return '该 Server 未配置任何密钥（直连下游）'
  const parts = [`在用 ${s.usableKeyCount} / ${s.keyCount}`]
  if (s.coolingKeyCount) parts.push(`冷却 ${s.coolingKeyCount}`)
  if (s.disabledKeyCount) parts.push(`弃用 ${s.disabledKeyCount}`)
  if (typeof s.keySuccessRate === 'number') parts.push(`成功率 ${formatPercent(s.keySuccessRate)}`)
  return parts.join(' · ')
}
