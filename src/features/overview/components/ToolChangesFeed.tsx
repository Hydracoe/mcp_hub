import { Icon } from '@/components/common/Icon'
import { EmptyState } from '@/components/common/States'
import { Badge } from '@/components/ui/badge'
import { Skeleton } from '@/components/ui/skeleton'
import { useToolChanges } from '@/lib/api/queries'
import { formatRelative } from '@/lib/format'
import type { ToolChangeAction, ToolChangeItem } from '@/lib/api/types'

/** 变更动作 → 徽标文案与语义色。新增要看见（可能需要启用），
 *  删除要警惕（AI 手里的工具名可能失效），启停是自有动作、弱化即可。 */
const ACTION_META: Record<ToolChangeAction, { label: string; variant: 'success' | 'warning' | 'error' | 'neutral' }> = {
  added: { label: '新增', variant: 'warning' },
  removed: { label: '删除', variant: 'error' },
  updated: { label: '更新', variant: 'neutral' },
  enabled: { label: '启用', variant: 'success' },
  disabled: { label: '禁用', variant: 'neutral' },
}

function ChangeRow({ item }: { item: ToolChangeItem }) {
  const meta = ACTION_META[item.action] ?? { label: item.action, variant: 'neutral' as const }

  return (
    <div className="grid grid-cols-12 items-center gap-3 px-4 py-2.5 text-[12px]">
      <div className="col-span-2">
        <Badge variant={meta.variant}>{meta.label}</Badge>
      </div>
      <div className="col-span-4 min-w-0">
        <span className="truncate font-mono text-foreground" title={item.toolName}>
          {item.toolName}
        </span>
      </div>
      <div className="col-span-3 min-w-0 truncate font-mono text-muted-foreground" title={item.serverKey}>
        {item.serverKey}
      </div>
      <div className="col-span-2 min-w-0 truncate text-muted-foreground" title={item.detail}>
        {item.actor === 'admin' ? '人工操作' : item.detail || '探活对账'}
      </div>
      <div className="col-span-1 text-right font-mono text-muted-foreground">
        {formatRelative(item.ts)}
      </div>
    </div>
  )
}

/**
 * 上游工具变更流水。
 *
 * 探活每轮都会按名对账，变更本身是自动生效的（描述/schema 就地刷新、
 * 删除即摘除），这张表的作用是让「上游悄悄改了什么」可见——出问题时
 * 第一眼就能看到是不是上游动过。
 */
export function ToolChangesFeed({ limit = 8 }: { limit?: number }) {
  const { data, isLoading, isError, error } = useToolChanges({ limit })
  const items = data?.items ?? []

  if (isLoading) {
    return (
      <div className="ui-card space-y-2 p-4">
        {Array.from({ length: 4 }).map((_, i) => (
          <Skeleton key={i} className="h-6 w-full" />
        ))}
      </div>
    )
  }

  if (isError) {
    return (
      <div className="ui-card flex items-center gap-2 px-4 py-3 text-[12px] text-muted-foreground">
        <Icon name="circle-alert" size={14} className="text-error-strong" />
        变更记录加载失败：{error instanceof Error ? error.message : '未知错误'}
      </div>
    )
  }

  if (items.length === 0) {
    return (
      <div className="ui-card">
        <EmptyState
          icon="scroll-text"
          title="暂无工具变更"
          description="探活发现上游工具新增、删除或描述变更时，会记录在这里。"
        />
      </div>
    )
  }

  return (
    <div className="ui-card overflow-hidden">
      <div className="grid grid-cols-12 gap-3 border-b border-separator px-4 py-1.5 text-[10.5px] font-semibold tracking-[0.06em] text-icon-muted uppercase">
        <div className="col-span-2">动作</div>
        <div className="col-span-4">工具</div>
        <div className="col-span-3">Server</div>
        <div className="col-span-2">来源</div>
        <div className="col-span-1 text-right">时间</div>
      </div>
      <div className="divide-y divide-separator">
        {items.map((item) => (
          <ChangeRow key={item.id} item={item} />
        ))}
      </div>
    </div>
  )
}
