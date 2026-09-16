import { Icon } from '@/components/common/Icon'
import { Badge } from '@/components/ui/badge'
import { Switch } from '@/components/ui/switch'
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
import type { ServerDetail, ServerToolItem } from '@/lib/api/types'
import { useRefreshTools, useSetToolEnabled, useTools } from '@/lib/api/queries'
import { toastError, toastSuccess } from '@/lib/toast'
import { formatInt } from '@/lib/format'

export interface ToolsInventoryProps {
  server: ServerDetail
}

export function ToolsInventory({ server }: ToolsInventoryProps) {
  const { data, isLoading } = useTools({ serverKey: server.key })
  const refresh = useRefreshTools()
  const setEnabled = useSetToolEnabled()

  // 真相源是详情里的 toolItems（带人工开关状态）；未探活、后端还没给
  // 明细时，退回原始名列表（此时没有状态可展示）。
  const items = server.toolItems ?? []
  const hasItems = items.length > 0
  const fallbackNames = !hasItems ? (server.tools ?? (data?.items ?? []).map((t) => t.name)) : []

  const declared = hasItems ? items.length : fallbackNames.length
  const enabledCount = server.enabledToolCount ?? items.filter((t) => t.enabled).length
  // 生效中 = 已启用且 server 已启用；server 停用时两个数才会分开。
  const exposedCount = server.exposedToolCount ?? enabledCount
  const disabledCount = Math.max(0, declared - enabledCount)

  const toggle = (item: ServerToolItem, next: boolean) => {
    setEnabled.mutate(
      { serverId: server.id, toolName: item.name, enabled: next },
      {
        onSuccess: () => toastSuccess(next ? '工具已启用' : '工具已禁用', item.exposedName),
        onError: (err) => toastError(err, next ? '启用失败' : '禁用失败'),
      },
    )
  }

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
            {refresh.isPending ? (
              <Icon name="refresh-cw" size={13} className="animate-spin" />
            ) : (
              <Icon name="refresh-cw" size={13} />
            )}
            刷新工具
          </button>
        </div>
      </div>
      <div className="form-section-body">
        <div className="mb-3 flex flex-wrap gap-2">
          <Stat label="下游声明" value={declared} />
          <Stat label="已启用" value={enabledCount} tone={enabledCount > 0 ? 'success' : 'neutral'} />
          <Stat label="生效中" value={exposedCount} tone={exposedCount > 0 ? 'brand' : 'neutral'} />
          <Stat label="已禁用" value={disabledCount} tone={disabledCount > 0 ? 'warning' : 'neutral'} />
        </div>

        {/* 这里已经身处 form-card 里，再包一层 .ui-table-wrap 会出现嵌套卡片；
            用 BareTable，横向滚动交给 .ui-table-scroll。 */}
        <div className="ui-table-scroll">
          {/* table-fixed + 每列定宽：拨开关会改单元格内容（对外名 ↔ "—"、徽标文案），
              auto 布局下列宽会跟着内容重新分配、整行列位置跳走；定宽列不随内容变。 */}
          <BareTable className="table-fixed w-full min-w-160">
            <TableHeader>
              <TableRow>
                <TableHead className="w-[30%]">原始工具名</TableHead>
                <TableHead className="w-[40%]">对外名</TableHead>
                <TableHead className="w-[16%]">状态</TableHead>
                <TableHead className="w-[14%] text-right">启用</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {isLoading ? (
                <TableSkeletonRows rows={5} cols={4} />
              ) : declared === 0 ? (
                <TableStateRow colSpan={4}>
                  <div className="px-6 py-8 text-center text-muted-foreground">
                    暂无工具（可能尚未探活或下游未返回工具清单）
                  </div>
                </TableStateRow>
              ) : hasItems ? (
                items.map((item) => {
                  const pending =
                    setEnabled.isPending &&
                    setEnabled.variables?.toolName === item.name &&
                    setEnabled.variables?.serverId === server.id
                  return (
                    <TableRow key={item.name}>
                      <TableCell className="truncate font-mono" title={item.name}>
                        {item.name}
                      </TableCell>
                      <TableCell
                        className="truncate font-mono text-muted-foreground"
                        title={item.enabled ? item.exposedName : undefined}
                      >
                        {item.enabled ? item.exposedName : '—'}
                      </TableCell>
                      <TableCell>
                        <StatusBadge item={item} />
                      </TableCell>
                      <TableCell className="text-right">
                        {/* 新工具默认禁用：这个开关就是「评审过、放行给 AI」的动作。 */}
                        <Switch
                          checked={item.enabled}
                          disabled={pending}
                          onCheckedChange={(next) => toggle(item, next)}
                          aria-label={`${item.enabled ? '禁用' : '启用'}工具 ${item.name}`}
                        />
                      </TableCell>
                    </TableRow>
                  )
                })
              ) : (
                // 未探活、只有名字时：没有开关状态可操作，等一次「刷新工具」。
                fallbackNames.map((name) => (
                  <TableRow key={name}>
                    <TableCell className="truncate font-mono" title={name}>
                      {name}
                    </TableCell>
                    <TableCell className="font-mono text-muted-foreground">—</TableCell>
                    <TableCell>
                      <Badge variant="neutral">未探活</Badge>
                    </TableCell>
                    <TableCell className="text-right">
                      <Switch checked={false} disabled aria-label={`工具 ${name} 未探活`} />
                    </TableCell>
                  </TableRow>
                ))
              )}
            </TableBody>
          </BareTable>
        </div>
        <p className="form-hint mt-2">
          新探活发现的工具默认禁用，需人工启用后才会进入 AI 的工具列表；
          工具的启停只有这一个开关。下游声明 {formatInt(declared)} 个、
          已启用 {formatInt(enabledCount)} 个、生效中 {formatInt(exposedCount)} 个
          （生效中 = 已启用且 server 已启用）。
        </p>
      </div>
    </div>
  )
}

function StatusBadge({ item }: { item: ServerToolItem }) {
  if (!item.enabled) {
    return <Badge variant="neutral">已禁用</Badge>
  }
  return (
    <Badge variant={item.available ? 'success' : 'error'}>
      {item.available ? '已启用' : '不可用'}
    </Badge>
  )
}

function Stat({
  label,
  value,
  tone = 'neutral',
}: {
  label: string
  value: number
  tone?: 'success' | 'warning' | 'neutral' | 'brand'
}) {
  const toneClass = {
    neutral: 'text-foreground',
    success: 'text-success-strong',
    warning: 'text-warning-strong',
    brand: 'text-brand-strong',
  }[tone]
  return (
    <span className="inline-flex items-center gap-1.5 rounded-md bg-fill-tertiary px-2.5 py-1 text-[12px]">
      <span className="text-icon-muted">{label}</span>
      <span className={`font-mono font-semibold tabular-nums ${toneClass}`}>{formatInt(value)}</span>
    </span>
  )
}
