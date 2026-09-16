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

/**
 * 工具市场的分类栏。
 *
 * 一条横向的分类条：分类独立于二级条件行，因为它决定工具对外的名字
 * （`<分类英文名>__<工具>`），是这一页的一级组织轴；塞进条件行会与
 * 「可用性」「Server」平级，读起来像是三个能互相替代的筛选项。
 *
 * 计数一律给**全集**：分类条要是跟着搜索词或 Server 变，用户就失去了
 * 「这个分类里有多少工具」——那恰恰是他决定点哪一项的唯一依据。
 * （二级条件的计数是另一个基数，见 ToolsPage 里的注释。）
 *
 * 排序保持字母序（中文走 zh-Hans-CN），不按计数排：分类是长期存在的坐标，
 * 顺序必须稳定。按计数排的话，一次调用量的波动就会让条里的项跳位置，
 * 肌肉记忆全部作废。
 *
 * 超出一行时横向滑动（形态与滚动都在 CSS：`.market-rail`），
 * 部分可见的最后一项就是「还能往右滑」的暗示。
 */
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
