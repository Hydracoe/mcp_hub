import { cn } from '@/lib/utils'

export interface RailItem {
  /** 空串 = 全部。URL 参数用同一个约定：默认值不写进 URL。 */
  value: string
  label: string
  count: number
}

interface Props {
  items: readonly RailItem[]
  value: string
  onChange: (value: string) => void
}

/** 工具市场分类栏（一级组织轴）：分类决定工具对外名字（<分类>__<工具>），
 *  故独立于二级条件行。计数给全集、排序走字母序（中文 zh-Hans-CN）以保持稳定，
 *  超出一行横向滑动，最后一项暗示可右滑。 */
export function CategoryRail({ items, value, onChange }: Props) {
  return (
    <nav className="market-rail ui-panel" aria-label="按分类浏览工具">
      <span className="market-rail-label">分类</span>
      {items.map((it) => {
        const active = it.value === value
        return (
          <button
            key={it.value || '__all__'}
            type="button"
            onClick={() => onChange(it.value)}
            aria-current={active ? 'true' : undefined}
            className={cn('market-rail-item', active && 'is-active')}
          >
            <span className="truncate">{it.label}</span>
            <span className="market-rail-count">{it.count}</span>
          </button>
        )
      })}
    </nav>
  )
}
