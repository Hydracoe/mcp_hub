import { Link } from 'react-router-dom'
import { TableCell, TableHead, TableRow } from '@/components/ui/table'
import { TableFrame } from '@/components/common/TableFrame'
import type { TableFrameProps } from '@/components/common/TableFrame'
import { SortHead, type SortDir } from '@/components/common/SortHead'
import { Switch } from '@/components/ui/switch'
import { Button } from '@/components/ui/button'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
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

/** 8 列宽表。table-fixed + 每列定宽，避免列宽随排序/翻页换批行而跳动。 */
const COLS = 8

/** 列宽一律用百分比，总和恒为 100%——定宽 px 会让表格在任意窗口都多出一条
 *  永远关不掉的横向滚动条。代价是窄窗口表头被截断（有 overflow 兜底）。
 *  传输列要装下最长的 streamable_http，压缩名称列给它让位；
 *  可见性列只放一个图标，3% 足够。 */
const COL_W = [
  'w-[18%]', // 名称 / Key
  'w-[3%]', // AI 可见性（仅图标）
  'w-[9%]', // 传输
  'w-[12%]', // 状态
  'w-[14%]', // 工具
  'w-[13%]', // 密钥
  'w-[13%]', // 延迟 / 检查
  'w-[18%]', // 操作
]

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
  /* 表头排序用 'asc'|'desc'，URL 状态用 1|-1，换算只在此处做。 */
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
          <TableHead className={COL_W[1]} aria-label="AI 可见性" />
          <TableHead className={COL_W[2]}>传输</TableHead>
          {head('状态', 'status', 3)}
          {head('工具', 'tools', 4)}
          {head('密钥', 'keys', 5)}
          {head('延迟 / 检查', 'latency', 6)}
          <TableHead className={cn(COL_W[7], 'text-right')}>操作</TableHead>
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

      {/* AI 可见性：启用中但 0 个生效工具时亮 eye-off，hover 说明原因。 */}
      <TableCell>
        {s.enabled && s.enabledToolCount === 0 ? (
          <Tooltip>
            <TooltipTrigger asChild>
              <button type="button" className="inline-flex" aria-label="AI 不可见">
                <Icon name="eye-off" size={15} className="text-warning-strong" />
              </button>
            </TooltipTrigger>
            <TooltipContent>
              AI 不可见：Server 启用中但没有任何生效工具（停用期间工具被级联禁用，重新启用不自动恢复）
            </TooltipContent>
          </Tooltip>
        ) : null}
      </TableCell>

      <TableCell>
        <TransportTag transport={s.transport} />
      </TableCell>

      <TableCell>
        {/* items-start：flex-col 默认 stretch 会把徽标拉满整列宽，看起来像一条长矩形。 */}
        <div className="flex flex-col items-start gap-1">
          {disabled ? <StoppedBadge /> : <ServerStatusBadge status={s.status} />}
          {s.draining ? <DrainingBadge inflight={s.drainingInflight} /> : null}
        </div>
      </TableCell>

      <TableCell>
        <button type="button" onClick={() => onOpenTools(s)} className="text-left hover:opacity-80" title={toolTitle(s)}>
          <span className="font-mono tabular-nums text-foreground">{toolSummary(s)}</span>
        </button>
      </TableCell>

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

      <TableCell>
        {/* 「从未探活」只看 lastCheckAt：探活失败时延迟为 0，不能据此误判成没测过。 */}
        {s.lastCheckAt ? (
          <div>
            {typeof s.lastLatencyMs === 'number' && s.lastLatencyMs > 0 ? (
              <span className="font-mono tabular-nums text-foreground">{s.lastLatencyMs}ms</span>
            ) : (
              <span className="text-muted-foreground">—</span>
            )}
            <div className="font-mono cell-sub text-muted-foreground">{formatRelative(s.lastCheckAt)}</div>
          </div>
        ) : (
          <span className="text-muted-foreground">尚未探活</span>
        )}
      </TableCell>

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
  if (s.enabled && s.enabledToolCount === 0)
    parts.push('Server 启用中但没有生效工具：停用期间工具被级联禁用，重新启用不自动恢复，AI 侧完全看不到')
  if (typeof s.toolSuccessRate === 'number') parts.push(`成功率 ${formatPercent(s.toolSuccessRate)}`)
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
