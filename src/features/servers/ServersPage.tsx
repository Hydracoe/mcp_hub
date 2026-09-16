import { useMemo, useState } from 'react'

import { Button } from '@/components/ui/button'
import { Icon } from '@/components/common/Icon'
import { PageLayout } from '@/components/layout/PageLayout'
import { Pagination } from '@/components/common/Pagination'
import { FilterChips, type FilterChipOption } from '@/components/common/FilterChips'
import { SearchInput } from '@/components/common/SearchInput'
import { ConfirmDialog } from '@/components/common/ConfirmDialog'
import { EmptyState } from '@/components/common/States'
import { paginate, sortRows, useListQueryState } from '@/lib/use-list-query-state'
import { useDeleteServer, useOverview, useServers, useSetServerEnabled, useTestAllServers, useTestServer } from '@/lib/api/queries'
import { formatDuration, formatInt } from '@/lib/format'
import { toastError, toastSuccess } from '@/lib/toast'
import type { ServerSummary } from '@/lib/api/types'

import { ServersTable } from './components/ServersTable'
import { ServerFormDialog } from './components/ServerFormDialog'
import { ServerToolsDialog } from './components/ServerToolsDialog'
import { ServerCredentialsDialog } from './components/ServerCredentialsDialog'
import { TestAllPanel } from './components/TestAllPanel'

type Filter = 'all' | 'healthy' | 'degraded' | 'offline' | 'draining' | 'disabled'

const SORT_KEYS = ['name', 'status', 'tools', 'keys', 'latency'] as const

const STATUS_ORDER: Record<string, number> = { offline: 0, degraded: 1, unknown: 2, healthy: 3 }

/**
 * 行内展开与「管理弹框」的分工，设计稿没有说清：
 * 列表页只做「看得见 + 启停 + 探活」，真正的写操作（编辑 / 工具 / 密钥）
 * 分别落在 Server 详情、工具市场、密钥池三个独立路由上（PRD §4.6）。
 * 弹框保留为只读预览 + 跳转入口。
 */
export function ServersPage() {
  const { data: overview } = useOverview()
  const { data, isLoading, error, refetch } = useServers()
  const setEnabled = useSetServerEnabled()
  const testOne = useTestServer()
  const testAll = useTestAllServers()
  const removeServer = useDeleteServer()

  const { state, setQ, setFilter, setPage, setPageSize, toggleSort } = useListQueryState({
    defaultSort: 'name',
    defaultDir: 1,
    sortKeys: SORT_KEYS,
  })

  const [formOpen, setFormOpen] = useState(false)
  const [toolsTarget, setToolsTarget] = useState<ServerSummary | null>(null)
  const [credsTarget, setCredsTarget] = useState<ServerSummary | null>(null)
  const [deleteTarget, setDeleteTarget] = useState<ServerSummary | null>(null)

  const all = data?.items ?? []

  const counts = useMemo(() => {
    return {
      all: all.length,
      healthy: all.filter((s) => s.enabled && s.status === 'healthy').length,
      degraded: all.filter((s) => s.enabled && s.status === 'degraded').length,
      offline: all.filter((s) => s.enabled && s.status === 'offline').length,
      draining: all.filter((s) => s.draining).length,
      disabled: all.filter((s) => !s.enabled).length,
    }
  }, [all])

  const filterOptions: readonly FilterChipOption<Filter>[] = [
    { value: 'all', label: '全部', count: counts.all },
    { value: 'healthy', label: '健康', tone: 'success', count: counts.healthy },
    { value: 'degraded', label: '降级', tone: 'warning', count: counts.degraded },
    { value: 'offline', label: '离线', tone: 'error', count: counts.offline },
    { value: 'draining', label: '停用中', tone: 'neutral', count: counts.draining },
    { value: 'disabled', label: '已停用', tone: 'neutral', count: counts.disabled },
  ]

  const filtered = useMemo(() => {
    const q = state.q.toLowerCase()
    return all.filter((s) => {
      // ⚠ 设计稿的筛选只覆盖 status，停用项会被「健康」筛掉却也能被「全部」混进来。
      // 这里把 enabled 与 draining 也纳入判定，保证每个 chip 的 count 与结果一致。
      if (state.filter === 'disabled') {
        if (s.enabled) return false
      } else if (state.filter === 'draining') {
        if (!s.draining) return false
      } else if (state.filter !== 'all') {
        if (!s.enabled || s.status !== state.filter) return false
      }
      if (!q) return true
      return (
        s.name.toLowerCase().includes(q) ||
        s.key.toLowerCase().includes(q) ||
        s.target.toLowerCase().includes(q) ||
        (s.description ?? '').toLowerCase().includes(q) ||
        (s.tags ?? []).some((t) => t.toLowerCase().includes(q))
      )
    })
  }, [all, state.filter, state.q])

  const sorted = useMemo(() => {
    const key = (s: ServerSummary) => {
      switch (state.sort) {
        case 'status':
          return STATUS_ORDER[s.status] ?? 9
        case 'tools':
          return s.toolCount
        case 'keys':
          return s.usableKeyCount
        case 'latency':
          return s.lastLatencyMs ?? Number.MAX_SAFE_INTEGER
        default:
          return s.name
      }
    }
    return sortRows(filtered, key, state.dir)
  }, [filtered, state.sort, state.dir])

  const page = paginate(sorted, state.page, state.pageSize)

  const onToggleEnabled = (s: ServerSummary, enabled: boolean) => {
    setEnabled.mutate(
      { id: s.id, enabled },
      {
        onSuccess: () => toastSuccess(enabled ? '已启用' : '已停用', `${s.key} · ${enabled ? '开始接收流量' : '停止摘取新流量，存量调用会收尾'}`),
        onError: (e) => toastError(e, enabled ? '启用失败' : '停用失败'),
      },
    )
  }

  const onTest = (s: ServerSummary) => {
    testOne.mutate(s.id, {
      onSuccess: (probe) => {
        if (probe.ok) {
          toastSuccess('探活通过', `${s.key} · L1–L3 全部通过 · ${formatDuration(probe.totalMs)}`)
        } else {
          toastError(
            new Error(probe.hint ?? probe.error ?? '探活失败'),
            `${s.key} 探活失败`,
          )
        }
      },
      onError: (e) => toastError(e, '探活请求失败'),
    })
  }

  const onTestAll = () => {
    testAll.mutate(undefined, {
      onSuccess: (r) =>
        toastSuccess(
          '全量测试完成',
          `健康 ${r.healthy} · 失败 ${r.failed} · 停用 ${r.disabled}（不计成败）· ${formatDuration(r.tookMs)}`,
        ),
      onError: (e) => toastError(e, '全量测试失败'),
    })
  }

  const onConfirmDelete = () => {
    if (!deleteTarget) return
    removeServer.mutate(deleteTarget.id, {
      onSuccess: () => {
        toastSuccess('已删除', `${deleteTarget.key}`)
        setDeleteTarget(null)
      },
      onError: (e) => toastError(e, '删除失败'),
    })
  }

  return (
    <PageLayout
      title="Server 列表"
      version={overview?.version}
      ready={overview?.ready}
      actions={
        <>
          <Button variant="secondary" size="sm" onClick={onTestAll} disabled={testAll.isPending}>
            <Icon name={testAll.isPending ? 'refresh-cw' : 'zap'} size={13} className={testAll.isPending ? 'animate-spin' : undefined} />
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

      {/* 缺口 B4：全量测试结果只有按钮没有落点，这里用内存里的一份报告兜住。 */}
      <section className="mt-4">
        <div className="mb-4 flex flex-wrap items-center justify-between gap-3">
          <div className="flex items-center gap-3">
            <span className="rounded-md bg-muted px-2 py-0.5 text-xs font-medium text-foreground tabular-nums">
              {formatInt(data?.total ?? all.length)} 个
            </span>
            <span className="text-xs text-muted-foreground">
              点「工具 / 密钥」可预览该 Server 的聚合结果，写操作在对应页面完成
            </span>
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <FilterChips value={state.filter as Filter} options={filterOptions} onChange={setFilter} />
            <SearchInput
              value={state.q}
              onChange={setQ}
              placeholder="搜索名称 / Key / 目标…"
              aria-label="搜索 Server"
              className="w-56"
            />
          </div>
        </div>

        {/* 外壳、加载态、空态、页脚统一由 TableFrame 承担：
            页面不再自己包 `.ui-table-wrap`（那会套出双描边），
            空表也保留表头，让「空的是什么形状」可见。 */}
        <ServersTable
          rows={page.rows}
          frame={{
            loading: isLoading,
            skeletonRows: 8,
            error: error
              ? { title: 'Server 列表加载失败', error, onRetry: () => void refetch() }
              : undefined,
            isEmpty: page.total === 0,
            empty: (
              <EmptyState
                icon="server"
                title={state.q || state.filter !== 'all' ? '没有匹配的 Server' : '还没有配置任何 Server'}
                description={
                  state.q || state.filter !== 'all'
                    ? '换一个筛选条件或清空搜索词试试。'
                    : '新增一个下游 MCP Server，接入聚合网关。'
                }
                action={
                  state.q || state.filter !== 'all' ? (
                    <Button
                      variant="secondary"
                      size="sm"
                      onClick={() => {
                        setQ('')
                        setFilter('all')
                      }}
                    >
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
          togglingId={setEnabled.isPending ? (setEnabled.variables?.id ?? null) : null}
          onTest={onTest}
          testingId={testOne.isPending ? (testOne.variables ?? null) : null}
          onOpenTools={setToolsTarget}
          onOpenCreds={setCredsTarget}
          onDelete={setDeleteTarget}
        />
      </section>

      <ServerFormDialog open={formOpen} onOpenChange={setFormOpen} />
      <ServerToolsDialog server={toolsTarget} open={Boolean(toolsTarget)} onOpenChange={(v) => !v && setToolsTarget(null)} />
      <ServerCredentialsDialog server={credsTarget} open={Boolean(credsTarget)} onOpenChange={(v) => !v && setCredsTarget(null)} />

      <ConfirmDialog
        open={Boolean(deleteTarget)}
        onOpenChange={(v) => !v && setDeleteTarget(null)}
        title={deleteTarget ? `删除 Server「${deleteTarget.name}」？` : '删除 Server？'}
        description={
          deleteTarget ? (
            <div className="space-y-2">
              <p>
                将从聚合网关撤下 <code className="code-chip">{deleteTarget.key}</code>，其暴露的{' '}
                <strong>{formatInt(deleteTarget.exposedToolCount)}</strong> 个工具会立即对客户端不可见。
              </p>
              {deleteTarget.keyCount > 0 ? (
                <p className="text-error-strong">
                  连带删除该 Server 的 {formatInt(deleteTarget.keyCount)} 把密钥，此操作不可撤销。
                </p>
              ) : null}
              <p className="text-muted-foreground">此操作不可撤销。如需暂时停止服务，请改用「停用」。</p>
            </div>
          ) : null
        }
        confirmText="删除"
        destructive
        pending={removeServer.isPending}
        onConfirm={onConfirmDelete}
      />
    </PageLayout>
  )
}
