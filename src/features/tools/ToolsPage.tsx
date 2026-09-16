import { useMemo, useState } from 'react'
import { useSearchParams } from 'react-router-dom'

import { Button } from '@/components/ui/button'
import { Combobox, type ComboboxOption } from '@/components/common/Combobox'
import { PageLayout } from '@/components/layout/PageLayout'
import { FilterChips, type FilterChipOption } from '@/components/common/FilterChips'
import { SearchInput } from '@/components/common/SearchInput'
import { Pagination } from '@/components/common/Pagination'
import { EmptyState, ErrorState, CardSkeleton } from '@/components/common/States'
import { Icon } from '@/components/common/Icon'
import { useOverview, useTools } from '@/lib/api/queries'
import { formatInt } from '@/lib/format'
import type { ToolEntry } from '@/lib/api/types'

import { CategoryRail, type RailItem } from './components/CategoryRail'
import { ToolGrid } from './components/ToolGrid'
import { ToolSchemaDialog } from './components/ToolSchemaDialog'

type Avail = 'all' | 'available' | 'unavailable'
const PAGE_SIZE = 12

/** 分类条里「未分类」那一项的值。真实分类由用户在管理页命名，撞不上双下划线。 */
const UNCATEGORIZED = '__none__'

/**
 * 工具市场。
 *
 * 布局是「分类条 + 条件行 + 网格」纵向三层：分类是一级导航，可用性 /
 * Server / 搜索是二级条件。此前这七个横条（说明条、区块标题、条件行、
 * Server 芯片云……）平铺在同一层，没有层级可言 —— 最大的那块还是
 * 十几个 Server 的芯片云，换行两三排像一面标签墙。
 *
 * 数据一次拉全量、在前端筛选。这是个自觉的取舍：`GET /api/tools` 只支持
 * `serverKey` 与 `q` 两个参数，而这一页还要按分类、按可用性筛，并且分类条
 * 与条件 chip 上都要显示计数 —— 计数必须建立在全集之上，否则「web-search 12」
 * 会随着分类筛选变成 3，导航就失去了指路的作用。
 * 等到注册表大到全量拉取不划算时，应把筛选下推到接口，而不是在这里加缓存。
 *
 * 筛选状态全部挂 URL（与调用日志页同一套约定）：刷新、后退、把链接发给同事
 * 都保持视图。默认值不写进 URL，否则每条链接都要拖一串空参数。
 */
export function ToolsPage() {
  const { data: overview } = useOverview()
  const { data, isLoading, error, refetch } = useTools()
  const [params, setParams] = useSearchParams()
  const [schemaTool, setSchemaTool] = useState<ToolEntry | null>(null)

  /* 空串 = 不筛。分类栏的「全部」也用空串表达，URL 里干脆不出现。 */
  const category = params.get('category') ?? ''
  const server = params.get('server') ?? ''
  const q = params.get('q') ?? ''
  const page = Math.max(1, Number(params.get('page')) || 1)
  /* URL 是可以手改的，非法值当默认处理，别让它渗进筛选逻辑。 */
  const availParam = params.get('avail')
  const avail: Avail =
    availParam === 'available' || availParam === 'unavailable' ? availParam : 'all'

  /* undefined / 空串 = 回到默认：把键从 URL 里删掉，而不是留一个空参数。 */
  const patch = (next: Record<string, string | number | undefined>) => {
    const p = new URLSearchParams(params)
    for (const [k, v] of Object.entries(next)) {
      if (v === undefined || v === '') p.delete(k)
      else p.set(k, String(v))
    }
    setParams(p, { replace: true })
  }

  /**
   * 改条件。页码一律打回第 1 页 —— 筛完停在「第 3 页」而结果只剩 1 页，
   * 是这类列表页最常见的空列表来源。统一在这里打回，免得每个调用点各写
   * 一遍（漏一个就是一个空白页）。
   */
  const setFilter = (next: Record<string, string | number | undefined>) =>
    patch({ ...next, page: undefined })

  const tools = useMemo(() => data?.items ?? [], [data])

  const railItems = useMemo<RailItem[]>(() => {
    const counts = new Map<string, number>()
    let uncategorized = 0
    for (const t of tools) {
      if (t.category) counts.set(t.category, (counts.get(t.category) ?? 0) + 1)
      else uncategorized += 1
    }
    const items: RailItem[] = [
      { value: '', label: '全部', count: tools.length },
      ...[...counts.entries()]
        .sort((a, b) => a[0].localeCompare(b[0], 'zh-Hans-CN'))
        .map(([name, count]) => ({ value: name, label: name, count })),
    ]
    // 只在真有未分类工具时才给入口，否则是个恒为 0 的死项。
    if (uncategorized > 0) {
      items.push({ value: UNCATEGORIZED, label: '未分类', count: uncategorized })
    }
    return items
  }, [tools])

  /* 当前分类里的工具。二级条件的计数与筛选都以它为基数 ——
     选了「数据库」之后，如果「不可用 3」还是全量数字，会和网格里的 6 个对不上。 */
  const inCategory = useMemo(() => {
    if (category === '') return tools
    if (category === UNCATEGORIZED) return tools.filter((t) => !t.category)
    return tools.filter((t) => t.category === category)
  }, [tools, category])

  const availCounts = useMemo(() => {
    const available = inCategory.filter((t) => t.available).length
    return { available, unavailable: inCategory.length - available }
  }, [inCategory])

  const availOptions: readonly FilterChipOption<Avail>[] = [
    { value: 'all', label: '全部', count: inCategory.length },
    { value: 'available', label: '可用', tone: 'success', count: availCounts.available },
    { value: 'unavailable', label: '不可用', tone: 'error', count: availCounts.unavailable },
  ]

  /* Server 数量不封顶，走带搜索的下拉（与调用日志页同一套约定）。
     hint 给工具数：这一页选 Server 的动机就是「谁的工具多」。 */
  const serverOptions = useMemo<ComboboxOption[]>(() => {
    const counts = new Map<string, number>()
    for (const t of inCategory) counts.set(t.serverKey, (counts.get(t.serverKey) ?? 0) + 1)
    return [
      { value: 'all', label: '全部 Server', hint: `${formatInt(inCategory.length)} 个工具` },
      ...[...counts.entries()]
        .sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))
        .map(([key, count]) => ({
          value: key,
          label: key,
          hint: `${formatInt(count)} 个工具`,
        })),
    ]
  }, [inCategory])

  const filtered = useMemo(() => {
    const needle = q.trim().toLowerCase()
    return inCategory.filter((t) => {
      if (server && t.serverKey !== server) return false
      if (avail === 'available' && !t.available) return false
      if (avail === 'unavailable' && t.available) return false
      if (!needle) return true
      return (
        t.exposedName.toLowerCase().includes(needle) ||
        t.name.toLowerCase().includes(needle) ||
        t.serverKey.toLowerCase().includes(needle) ||
        (t.category ?? '').toLowerCase().includes(needle) ||
        (t.description ?? '').toLowerCase().includes(needle)
      )
    })
  }, [inCategory, server, avail, q])

  const pages = Math.max(1, Math.ceil(filtered.length / PAGE_SIZE))
  const current = Math.min(page, pages)
  const rows = filtered.slice((current - 1) * PAGE_SIZE, current * PAGE_SIZE)

  /* 分类也算「筛选中」：清空筛选要能把人带回全景，否则点了「清空」却还在
     某个分类里，会以为按钮坏了。 */
  const hasFilter = category !== '' || server !== '' || avail !== 'all' || q.trim() !== ''

  const resetAll = () =>
    patch({ category: undefined, server: undefined, avail: undefined, q: undefined, page: undefined })

  return (
    <PageLayout
      title="工具市场"
      version={overview?.version}
      ready={overview?.ready}
    >
      {error ? (
        <ErrorState title="工具清单加载失败" error={error} onRetry={() => void refetch()} />
      ) : isLoading ? (
        <CardSkeleton count={6} />
      ) : (
        <div className="market-layout">
          <CategoryRail
            items={railItems}
            value={category}
            onChange={(v) => setFilter({ category: v || undefined })}
          />

          {/* min-w-0：网格是 grid 子项，不给它收缩权的话宽表会把右列撑破 */}
          <div className="min-w-0">
            {/* 二级条件收成一行。三个控件都是 32px（chips 走 size="md"），
                否则同一行里 chip 26px、下拉 32px，基线看着就是散的。 */}
            <div className="mb-4 flex flex-wrap items-center gap-3">
              <FilterChips
                size="md"
                value={avail}
                options={availOptions}
                onChange={(v) => setFilter({ avail: v === 'all' ? undefined : v })}
              />
              <Combobox
                value={server || 'all'}
                onChange={(v) => setFilter({ server: v === 'all' ? undefined : v })}
                options={serverOptions}
                className="w-[200px]"
                placeholder="全部 Server"
                searchPlaceholder="搜索 Server key…"
                emptyText="没有匹配的 Server"
                aria-label="按 Server 筛选"
              />
              <SearchInput
                value={q}
                onChange={(v) => setFilter({ q: v || undefined })}
                placeholder="搜索工具名 / 描述…"
                aria-label="搜索工具"
                className="ml-auto w-64"
              />
              {/* 刷新收成图标钮：动作低频（数据在窗口聚焦时本就会重取），
                  不值得一个带字的按钮常驻顶部标题行。 */}
              <Button
                variant="ghost"
                size="icon"
                onClick={() => void refetch()}
                disabled={isLoading}
                aria-label="刷新工具清单"
                title="刷新工具清单"
              >
                <Icon
                  name="refresh-cw"
                  size={14}
                  className={isLoading ? 'animate-spin' : undefined}
                />
              </Button>
              {hasFilter ? (
                <Button variant="ghost" size="sm" onClick={resetAll}>
                  <Icon name="x" size={12} />
                  清空筛选
                </Button>
              ) : null}
            </div>

            {filtered.length === 0 ? (
              <div className="ui-panel">
                <EmptyState
                  icon="wrench"
                  title={hasFilter ? '没有匹配的工具' : '聚合池里还没有工具'}
                  description={
                    hasFilter
                      ? '换一个条件，或者回到左栏的「全部」看看。'
                      : '先在 Server 列表里接入下游，工具会在探活通过后进入聚合池。'
                  }
                  action={
                    hasFilter ? (
                      <Button variant="secondary" size="sm" onClick={resetAll}>
                        清空筛选
                      </Button>
                    ) : undefined
                  }
                />
              </div>
            ) : (
              <>
                <ToolGrid items={rows} onViewSchema={setSchemaTool} />
                <Pagination
                  className="mt-4"
                  standalone
                  page={current}
                  pageSize={PAGE_SIZE}
                  total={filtered.length}
                  onPageChange={(p) => patch({ page: p === 1 ? undefined : p })}
                  unit="个工具"
                />
              </>
            )}
          </div>
        </div>
      )}

      <ToolSchemaDialog
        tool={schemaTool}
        open={Boolean(schemaTool)}
        onOpenChange={(v) => !v && setSchemaTool(null)}
      />
    </PageLayout>
  )
}
