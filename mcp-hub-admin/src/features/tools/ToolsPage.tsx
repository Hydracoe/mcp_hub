import { useState } from 'react'

import { Button } from '@/components/ui/button'
import { Combobox } from '@/components/common/Combobox'
import { FilterChips } from '@/components/common/FilterChips'
import {
  FilterField,
  FilterGrid,
  FilterPanel,
  FilterSearch,
  FilterSummary,
  type FilterSummaryItem,
} from '@/components/common/FilterPanel'
import { Icon } from '@/components/common/Icon'
import { PageLayout } from '@/components/layout/PageLayout'
import { Pagination } from '@/components/common/Pagination'
import { SearchInput } from '@/components/common/SearchInput'
import { Section } from '@/components/common/Section'
import { EmptyState, ErrorState, CardSkeleton } from '@/components/common/States'
import { formatInt } from '@/lib/format'
import type { ToolEntry } from '@/lib/api/types'

import { CategoryRail } from './components/CategoryRail'
import { ToolGrid } from './components/ToolGrid'
import { ToolSchemaDialog } from './components/ToolSchemaDialog'
import { useToolsPage } from './use-tools-page'

/** 工具管理：左栏分类为一级导航，右侧启用状态/可用性/Server/搜索为二级条件。
 *  被禁用的工具也留在列表里——开关必须和列表在一起。 */
export function ToolsPage() {
  const {
    overview,
    list,
    filters,
    railItems,
    stateOptions,
    availOptions,
    serverOptions,
    rows,
    filteredCount,
    pageSize,
    setFilter,
    patch,
    resetAll,
  } = useToolsPage()

  // 纯视图状态，没有副作用，留在组件里。
  const [schemaTool, setSchemaTool] = useState<ToolEntry | null>(null)

  const activeFilters: FilterSummaryItem[] = []
  if (filters.category) activeFilters.push({ key: 'category', label: `分类 ${filters.category}` })
  if (filters.state !== 'all')
    activeFilters.push({
      key: 'state',
      label: stateOptions.find((o) => o.value === filters.state)?.label ?? filters.state,
    })
  if (filters.avail !== 'all')
    activeFilters.push({
      key: 'avail',
      label: `下游${filters.avail === 'available' ? '可用' : '不可用'}`,
    })
  if (filters.server) activeFilters.push({ key: 'server', label: `Server ${filters.server}` })
  if (filters.q) activeFilters.push({ key: 'q', label: `“${filters.q}”` })

  return (
    <PageLayout title="工具管理" version={overview?.version}>
      {list.error ? (
        <ErrorState title="工具清单加载失败" error={list.error} onRetry={() => void list.refetch()} />
      ) : list.isLoading ? (
        <CardSkeleton count={6} />
      ) : (
        <div className="market-layout">
          <CategoryRail
            items={railItems}
            value={filters.category}
            onChange={(v) => setFilter({ category: v || undefined })}
          />

          {/* min-w-0：网格是 grid 子项，不给它收缩权的话宽表会把右列撑破 */}
          <div className="min-w-0">
            <Section
              title="工具查询"
              hint={
                <span className="inline-flex items-center gap-1.5 text-xs text-muted-foreground">
                  <Icon name="funnel" size={14} /> 分类在左栏切换 · 共 {formatInt(filteredCount)} 个工具
                </span>
              }
              actions={
                <Button
                  variant="ghost"
                  size="icon-sm"
                  aria-label="刷新"
                  onClick={() => void list.refetch()}
                >
                  <Icon name="refresh-cw" size={14} />
                </Button>
              }
            >
              <FilterPanel>
                <FilterGrid columns={3}>
                  {/* 主条件放最左：这是本页唯一会写回后端的筛选——拨一下开关
                      就改了 AI 能看到什么。 */}
                  <FilterField label="启用状态">
                    <FilterChips
                      size="md"
                      value={filters.state}
                      options={stateOptions}
                      onChange={(v) => setFilter({ state: v === 'all' ? undefined : v })}
                    />
                  </FilterField>

                  <FilterField label="下游可用性">
                    <FilterChips
                      size="md"
                      value={filters.avail}
                      options={availOptions}
                      onChange={(v) => setFilter({ avail: v === 'all' ? undefined : v })}
                    />
                  </FilterField>

                  {/* Server 数量不封顶，走带搜索的下拉。 */}
                  <FilterField label="Server">
                    <Combobox
                      value={filters.server || 'all'}
                      onChange={(v) => setFilter({ server: v === 'all' ? undefined : v })}
                      options={serverOptions}
                      className="w-full"
                      placeholder="全部 Server"
                      fallbackLabel={filters.server || '全部 Server'}
                      searchPlaceholder="搜索 Server key…"
                      emptyText="没有匹配的 Server"
                      aria-label="按 Server 筛选"
                    />
                  </FilterField>
                </FilterGrid>

                <FilterSearch>
                  <SearchInput
                    value={filters.q}
                    onChange={(v) => setFilter({ q: v || undefined })}
                    placeholder="搜索工具名 / 描述…"
                    aria-label="搜索工具"
                    className="flex-1"
                  />
                  <button
                    type="button"
                    className="ui-btn ui-btn-ghost ui-btn-sm rounded-full"
                    onClick={resetAll}
                  >
                    <Icon name="rotate-cw" size={14} />
                    重置
                  </button>
                </FilterSearch>

                <FilterSummary
                  items={activeFilters}
                  onRemove={(key) => {
                    if (key === 'category') setFilter({ category: undefined })
                    else if (key === 'state') setFilter({ state: undefined })
                    else if (key === 'avail') setFilter({ avail: undefined })
                    else if (key === 'server') setFilter({ server: undefined })
                    else setFilter({ q: undefined })
                  }}
                  onClear={resetAll}
                />
              </FilterPanel>
            </Section>

            <div className="mt-4">
              {filteredCount === 0 ? (
                <div className="ui-panel">
                  <EmptyState
                    icon="wrench"
                    title={filters.hasFilter ? '没有匹配的工具' : '聚合池里还没有工具'}
                    description={
                      filters.hasFilter
                        ? '换一个条件，或者回到左栏的「全部」看看。'
                        : '先在 Server 列表里接入下游，工具会在探活通过后进入聚合池。'
                    }
                    action={
                      filters.hasFilter ? (
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
                    page={filters.page}
                    pageSize={pageSize}
                    total={filteredCount}
                    onPageChange={(p) => patch({ page: p === 1 ? undefined : p })}
                    unit="个工具"
                  />
                </>
              )}
            </div>
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
