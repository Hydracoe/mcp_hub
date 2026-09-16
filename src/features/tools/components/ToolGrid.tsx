import { Link } from 'react-router-dom'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Icon } from '@/components/common/Icon'
import { Dash } from '@/components/common/KeyValue'
import { formatInt, formatPercent } from '@/lib/format'
import type { ToolEntry } from '@/lib/api/types'

/**
 * 工具卡片。
 *
 * 卡片上同时出现两个名字，是这一页最容易搞错的地方：
 * `exposedName` 是客户端实际要调的那个（带 `<serverKey>__` 前缀），
 * `name` 是下游认的名字。凡是展示给「要复制去用」的地方，一律用前者。
 */
export function ToolGrid({
  items,
  onViewSchema,
}: {
  items: ToolEntry[]
  onViewSchema: (tool: ToolEntry) => void
}) {
  return (
    <div className="tool-grid">
      {items.map((t) => (
        <article key={`${t.serverId}:${t.exposedName}`} className="ui-card flex flex-col p-4">
          <div className="flex items-start justify-between gap-2">
            <div className="min-w-0">
              <h3 className="truncate font-mono text-[13px] font-semibold text-foreground" title={t.exposedName}>
                {t.exposedName}
              </h3>
              <p className="mt-0.5 truncate font-mono text-[11px] text-icon-muted" title={t.name}>
                下游名 {t.name}
              </p>
            </div>
            {t.available ? (
              <Badge variant="success">available</Badge>
            ) : (
              <Badge variant="error">不可用</Badge>
            )}
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
            {t.enabled === false ? <Badge variant="neutral">已停用</Badge> : null}
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
          <div className="mt-auto flex items-center gap-2 border-t border-separator pt-3">
            <Button variant="outline" size="xs" onClick={() => onViewSchema(t)}>
              <Icon name="code" size={12} />
              Schema
            </Button>
          </div>
        </article>
      ))}
    </div>
  )
}
