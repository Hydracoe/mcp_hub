import { PageLayout } from '@/components/layout/PageLayout'
import { Section } from '@/components/common/Section'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { Pagination } from '@/components/common/Pagination'
import { EmptyState } from '@/components/common/States'
import { Icon } from '@/components/common/Icon'
import { ConfirmDialog } from '@/components/common/ConfirmDialog'
import { formatRelative } from '@/lib/format'

import { LogFilterBar } from './components/LogFilterBar'
import { LogTable } from './components/LogTable'
import { LogDetailSheet } from './components/LogDetailSheet'
import { KeyEventsTable } from './components/KeyEventsTable'
import { useLogsPage } from './use-logs-page'

/** 调用日志。两个页签共用一条筛选栏，`serverId` 对两个视图都成立，
 *  所以切成「密钥事件」时筛选不丢。 */
export function LogsPage() {
  const {
    overview,
    logs,
    servers,
    callers,
    filters,
    tab,
    setTab,
    live,
    setLive,
    detail,
    clearDialog,
    openDetail,
    onConfirmClear,
  } = useLogsPage()

  const { values, page, size, sort, dir, query, set, toggleSort, reset } = filters

  return (
    <PageLayout title="调用日志" version={overview?.version}>
      <Section
        title="日志查询"
        hint={
          <span className="inline-flex items-center gap-1.5 text-xs text-muted-foreground">
            <Icon name="funnel" size={14} /> 按 Server / 调用方 / 状态 / 错误类型筛选 · 点表头「时间 /
            耗时 / 状态」排序
          </span>
        }
      >
        <LogFilterBar
          servers={servers}
          callers={callers}
          values={values}
          set={set}
          reset={reset}
          onClear={() => clearDialog.setOpen(true)}
          live={live}
          onLiveChange={setLive}
          lastRefresh={logs.dataUpdatedAt || undefined}
        />
      </Section>

      <Tabs value={tab} onValueChange={(v) => setTab(v as 'logs' | 'keys')} className="mt-5">
        <TabsList>
          <TabsTrigger value="logs">调用日志</TabsTrigger>
          <TabsTrigger value="keys">密钥事件日志</TabsTrigger>
        </TabsList>

        <TabsContent value="logs" className="mt-4">
          {logs.pausedByOverlay ? (
            <div className="mb-3 flex items-center gap-2 rounded-lg border border-separator bg-fill-tertiary px-3 py-2 text-xs text-muted-foreground">
              <Icon name="circle-pause" size={14} />
              {logs.pausedReason}
              {logs.dataUpdatedAt ? ` · 上次刷新 ${formatRelative(logs.dataUpdatedAt)}` : ''}
            </div>
          ) : null}

          <LogTable
            items={logs.items}
            onRowClick={openDetail}
            sort={sort}
            dir={dir}
            onSort={toggleSort}
            frame={{
              loading: logs.loading,
              skeletonRows: 8,
              error: logs.error
                ? { title: '调用日志加载失败', error: logs.error, onRetry: () => void logs.refetch() }
                : undefined,
              isEmpty: logs.total === 0,
              empty: (
                <EmptyState
                  icon="scroll-text"
                  title="没有匹配的调用日志"
                  description="尝试放宽筛选条件，或重置当前筛选。"
                />
              ),
              footer: (
                <Pagination
                  page={page}
                  pageSize={size}
                  total={logs.total}
                  unit="条"
                  onPageChange={(p) => set({ page: p })}
                  onPageSizeChange={(s) => set({ size: s, page: 1 })}
                />
              ),
            }}
          />
        </TabsContent>

        <TabsContent value="keys" className="mt-4">
          {/* 密钥事件与调用日志共用 serverId 筛选，切页签不丢条件。 */}
          <KeyEventsTable serverId={query.serverId} enabled={logs.polling} />
        </TabsContent>
      </Tabs>

      <LogDetailSheet log={detail.selected} open={detail.open} onOpenChange={detail.setOpen} />

      <ConfirmDialog
        open={clearDialog.open}
        onOpenChange={clearDialog.setOpen}
        title="清空调用日志"
        description="将删除网关记录的全部调用日志，且不可恢复。确定继续吗？"
        confirmText="清空日志"
        destructive
        pending={clearDialog.pending}
        onConfirm={onConfirmClear}
      />
    </PageLayout>
  )
}
