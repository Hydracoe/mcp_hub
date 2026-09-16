import { Button } from '@/components/ui/button'
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
import { EmptyState } from '@/components/common/States'

import { ServersTable } from './components/ServersTable'
import { ServerFormDialog } from './components/server-form'
import { ServerToolsDialog } from './components/ServerToolsDialog'
import { ServerCredentialsDialog } from './components/ServerCredentialsDialog'
import { ServerDeleteDialog } from './components/ServerDeleteDialog'
import { TestAllPanel } from './components/TestAllPanel'
import { useServersPage } from './use-servers-page'

/** Server 列表。这一页只管「看得见 + 启停 + 探活」，编辑 / 工具 / 密钥
 *  分别落在详情页、工具市场、密钥池，这里的弹框只做只读预览。 */
export function ServersPage() {
  const {
    overview,
    list,
    query,
    filterOptions,
    page,
    dialogs,
    busy,
    onToggleEnabled,
    onTest,
    onTestAll,
    onConfirmDelete,
  } = useServersPage()

  const { state, filter, setQ, setFilter, setPage, setPageSize, toggleSort, isFiltered } = query
  const { formOpen, setFormOpen, toolsTarget, setToolsTarget, credsTarget, setCredsTarget } = dialogs
  const { deleteTarget, setDeleteTarget } = dialogs

  const clearFilters = () => {
    setQ('')
    setFilter('all')
  }

  const activeFilters: FilterSummaryItem[] = []
  if (filter !== 'all') {
    activeFilters.push({
      key: 'filter',
      label: `状态 ${filterOptions.find((o) => o.value === filter)?.label ?? filter}`,
    })
  }
  if (state.q) activeFilters.push({ key: 'q', label: `“${state.q}”` })

  return (
    <PageLayout
      title="Server 列表"
      version={overview?.version}
      actions={
        <>
          <Button variant="secondary" size="sm" onClick={onTestAll} disabled={busy.testAllPending}>
            <Icon
              name={busy.testAllPending ? 'refresh-cw' : 'zap'}
              size={13}
              className={busy.testAllPending ? 'animate-spin' : undefined}
            />
            全量测试
          </Button>
          <Button variant="primary" size="sm" onClick={() => setFormOpen(true)}>
            <Icon name="plus" size={13} />
            新增 Server
          </Button>
        </>
      }
    >
      <TestAllPanel />

      <Section
        className="mt-4"
        title="Server 查询"
        hint={
          <span className="inline-flex items-center gap-1.5 text-xs text-muted-foreground">
            <Icon name="funnel" size={14} /> 按状态筛选 · 点表头「名称 / 状态 / 工具 / 延迟」排序
          </span>
        }
      >
        <FilterPanel
        >
          <FilterGrid columns={1}>
            <FilterField label="状态">
              <FilterChips size="md" value={filter} options={filterOptions} onChange={setFilter} />
            </FilterField>
          </FilterGrid>

          <FilterSearch>
            <SearchInput
              value={state.q}
              onChange={setQ}
              placeholder="搜索名称 / Key / 目标…"
              aria-label="搜索 Server"
              className="flex-1"
            />
            <button
              type="button"
              className="ui-btn ui-btn-ghost ui-btn-sm rounded-full"
              onClick={clearFilters}
            >
              <Icon name="rotate-cw" size={14} />
              重置
            </button>
          </FilterSearch>

          <FilterSummary
            items={activeFilters}
            onRemove={(key) => (key === 'filter' ? setFilter('all') : setQ(''))}
            onClear={clearFilters}
          />
        </FilterPanel>
      </Section>

      <div className="mt-4">
        <ServersTable
          rows={page.rows}
          frame={{
            loading: list.isLoading,
            skeletonRows: 8,
            error: list.error
              ? { title: 'Server 列表加载失败', error: list.error, onRetry: () => void list.refetch() }
              : undefined,
            isEmpty: page.total === 0,
            empty: (
              <EmptyState
                icon="server"
                title={isFiltered ? '没有匹配的 Server' : '还没有配置任何 Server'}
                description={
                  isFiltered
                    ? '换一个筛选条件或清空搜索词试试。'
                    : '新增一个下游 MCP Server，接入聚合网关。'
                }
                action={
                  isFiltered ? (
                    <Button variant="secondary" size="sm" onClick={clearFilters}>
                      清空筛选
                    </Button>
                  ) : (
                    <Button variant="primary" size="sm" onClick={() => setFormOpen(true)}>
                      新增 Server
                    </Button>
                  )
                }
              />
            ),
            footer: (
              <Pagination
                page={page.page}
                pageSize={state.pageSize}
                total={page.total}
                onPageChange={setPage}
                onPageSizeChange={setPageSize}
                unit="个 Server"
              />
            ),
          }}
          sort={state.sort}
          dir={state.dir}
          onSort={toggleSort}
          onToggleEnabled={onToggleEnabled}
          togglingId={busy.togglingId}
          onTest={onTest}
          testingId={busy.testingId}
          onOpenTools={setToolsTarget}
          onOpenCreds={setCredsTarget}
          onDelete={setDeleteTarget}
        />
      </div>

      <ServerFormDialog open={formOpen} onOpenChange={setFormOpen} />
      <ServerToolsDialog
        server={toolsTarget}
        open={Boolean(toolsTarget)}
        onOpenChange={(v) => !v && setToolsTarget(null)}
      />
      <ServerCredentialsDialog
        server={credsTarget}
        open={Boolean(credsTarget)}
        onOpenChange={(v) => !v && setCredsTarget(null)}
      />
      <ServerDeleteDialog
        server={deleteTarget}
        pending={busy.deleting}
        onOpenChange={(v) => !v && setDeleteTarget(null)}
        onConfirm={onConfirmDelete}
      />
    </PageLayout>
  )
}
