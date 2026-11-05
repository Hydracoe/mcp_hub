import { useState } from 'react'
import { Link } from 'react-router-dom'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Switch } from '@/components/ui/switch'
import { Icon } from '@/components/common/Icon'
import { ConfirmDialog } from '@/components/common/ConfirmDialog'
import { Dash } from '@/components/common/KeyValue'
import { useServers, useSetToolEnabled } from '@/lib/api/queries'
import { toastError, toastSuccess } from '@/lib/toast'
import { cn } from '@/lib/utils'
import { formatInt, formatPercent } from '@/lib/format'
import type { ToolEntry } from '@/lib/api/types'

/** 工具卡片。exposedName 是客户端调用的名（带 serverKey__ 前缀），name 是下游认的名；展示用前者。
 *
 *  卡片右下的开关是本页唯一会写回后端的动作：新工具默认禁用（上游随手加的工具
 *  不该直接裸奔到 AI 面前），人工放行才算数。被禁用的工具仍留在列表里——
 *  关掉一个工具不该等于把它弄丢了。 */
export function ToolGrid({
  items,
  onViewSchema,
}: {
  items: ToolEntry[]
  onViewSchema: (tool: ToolEntry) => void
}) {
  const setEnabled = useSetToolEnabled()
  // 判断"是不是该 server 最后一个已启用工具"要用 server 级的计数，
  // 本页的 ToolEntry 只带单工具状态，从 servers 列表（已在轮询）取。
  const { data: servers } = useServers()
  const [confirming, setConfirming] = useState<ToolEntry | null>(null)

  const doSetTool = (tool: ToolEntry, next: boolean) =>
    setEnabled.mutate(
      { serverId: tool.serverId, toolName: tool.name, enabled: next },
      {
        onSuccess: () => toastSuccess(next ? '工具已启用' : '工具已禁用', tool.exposedName),
        onError: (err) => toastError(err, next ? '启用失败' : '禁用失败'),
      },
    )

  const toggle = (tool: ToolEntry, next: boolean) => {
    if (!next && isLastEnabledTool(tool)) {
      setConfirming(tool)
      return
    }
    doSetTool(tool, next)
  }

  const isLastEnabledTool = (tool: ToolEntry) => {
    if (tool.enabled === false) return false
    const sv = servers?.items.find((s) => s.id === tool.serverId)
    return !!sv && sv.enabled && sv.enabledToolCount === 1
  }

  return (
    <>
      <div className="tool-grid">
        {items.map((t) => {
          // 未启用的用虚线边框标出来：一眼扫过去能看出
          // "这一屏里有几个是没生效的"，不用逐个读徽标。
          const hidden = t.enabled === false
          const busy =
            setEnabled.isPending &&
            setEnabled.variables?.serverId === t.serverId &&
            setEnabled.variables?.toolName === t.name

          return (
            <article
              key={`${t.serverId}:${t.exposedName}`}
              className={cn('ui-card flex flex-col p-4', hidden && 'border-dashed')}
            >
              <div className="flex items-start justify-between gap-2">
                <div className="min-w-0">
                  <h3
                    className="truncate font-mono text-[13px] font-semibold text-foreground"
                    title={t.exposedName}
                  >
                    {t.exposedName}
                  </h3>
                  <p className="mt-0.5 truncate font-mono text-[11px] text-icon-muted" title={t.name}>
                    下游名 {t.name}
                  </p>
                </div>
                <StatusBadge tool={t} />
              </div>

              {/* 两行封顶：描述高度是卡片高矮不齐的另一个来源，超长靠悬停（title）。 */}
              <p
                className="mt-2.5 line-clamp-2 min-h-[2.4em] text-[12px] leading-relaxed text-muted-foreground"
                title={t.description ?? undefined}
              >
                {t.description ?? '下游未提供描述。'}
              </p>

              <div className="mt-3 flex flex-wrap items-center gap-x-3 gap-y-1 text-[11.5px] text-muted-foreground">
                <Link
                  to={`/servers/${t.serverId}`}
                  className="inline-flex items-center gap-1 font-mono text-link hover:underline"
                >
                  <Icon name="server" size={12} />
                  {t.serverKey}
                </Link>
                {t.category ? <span className="chip chip-quiet">{t.category}</span> : null}
              </div>

              <div className="mt-2 flex flex-wrap items-center gap-x-4 gap-y-1 font-mono text-[11.5px] tabular-nums text-muted-foreground">
                <span>
                  调用 <span className="text-foreground">{formatInt(t.callCount)}</span>
                </span>
                <span>
                  成功率{' '}
                  <span className="text-foreground">
                    {typeof t.successRate === 'number' ? formatPercent(t.successRate) : <Dash />}
                  </span>
                </span>
              </div>

              {/* mt-auto 贴底：配合外层 flex-col，让一行三张卡片即使内容高矮
                不齐，操作按钮也落在同一条线上。 */}
              <div className="mt-auto flex items-center justify-between gap-2 border-t border-separator pt-3">
                <Button variant="outline" size="xs" onClick={() => onViewSchema(t)}>
                  <Icon name="code" size={12} />
                  Schema
                </Button>
                <label className="flex items-center gap-2 text-[11.5px] text-muted-foreground">
                  {t.enabled === false ? '已禁用' : '已启用'}
                  <Switch
                    checked={t.enabled !== false}
                    disabled={busy}
                    onCheckedChange={(next) => toggle(t, next)}
                    aria-label={`${t.enabled === false ? '启用' : '禁用'}工具 ${t.exposedName}`}
                  />
                </label>
              </div>
            </article>
          )
        })}
      </div>

      <ConfirmDialog
        open={confirming !== null}
        onOpenChange={(open) => {
          if (open) return
          // 取消（含 Esc）不执行任何动作。
          setConfirming(null)
        }}
        title="禁用后该 server 将自动停用"
        description={
          <>
            「{confirming?.exposedName}」是 server{' '}
            <span className="font-mono">{confirming?.serverKey}</span>{' '}
            最后一个已启用工具。禁用后该 server 没有任何可用工具，将被自动停用：
            探活停止、不再计入健康统计；工具清单与配置保留，随时可重新启用。
          </>
        }
        confirmText="禁用工具并停用 server"
        cancelText="取消"
        destructive
        pending={setEnabled.isPending}
        onConfirm={() => {
          const tool = confirming
          setConfirming(null)
          if (tool) doSetTool(tool, false)
        }}
      />
    </>
  )
}

/** 状态徽标。开关关着 = 对 AI 不可见；开着但下游连不上要单独标出。 */
function StatusBadge({ tool }: { tool: ToolEntry }) {
  if (tool.enabled === false) {
    return tool.pending ? <Badge variant="warning">待启用</Badge> : <Badge variant="neutral">已禁用</Badge>
  }
  return tool.available ? <Badge variant="success">已启用</Badge> : <Badge variant="error">下游不可用</Badge>
}
