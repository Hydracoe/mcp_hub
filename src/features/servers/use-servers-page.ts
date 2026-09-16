import { useMemo, useState } from 'react'

import type { FilterChipOption } from '@/components/common/FilterChips'
import { useListQueryState } from '@/lib/hooks/use-list-query-state'
import { paginate, sortRows } from '@/lib/list-utils'
import {
  useDeleteServer,
  useOverview,
  useServers,
  useSetServerEnabled,
  useTestAllServers,
  useTestServer,
} from '@/lib/api/queries'
import { formatDuration } from '@/lib/format'
import { toastError, toastSuccess } from '@/lib/toast'
import type { ServerSummary } from '@/lib/api/types'

/** Server 列表页的状态机：取数、筛选、排序、分页与四个弹框的开合。 */

export type ServerFilter = 'all' | 'healthy' | 'degraded' | 'offline' | 'draining' | 'disabled'

const SORT_KEYS = ['name', 'status', 'tools', 'keys', 'latency'] as const

/** 状态排序的权重：越靠前越需要处理。unknown 排在有结论的之后。 */
const STATUS_ORDER: Record<string, number> = { offline: 0, degraded: 1, unknown: 2, healthy: 3 }

/** 排序键。纯函数，单测可以直接打。 */
export const serverSortKey = (sort: string) => {
  switch (sort) {
    case 'status':
      return (s: ServerSummary) => STATUS_ORDER[s.status] ?? 9
    case 'tools':
      return (s: ServerSummary) => s.toolCount
    case 'keys':
      return (s: ServerSummary) => s.usableKeyCount
    case 'latency':
      return (s: ServerSummary) => s.lastLatencyMs ?? Number.MAX_SAFE_INTEGER
    default:
      return (s: ServerSummary) => s.name
  }
}

/** 筛选：把 enabled 与 draining 也纳入判定，保证每个 chip 的 count 与结果一致。 */
export const matchesServerFilter = (s: ServerSummary, filter: string, q: string) => {
  if (filter === 'disabled') {
    if (s.enabled) return false
  } else if (filter === 'draining') {
    if (!s.draining) return false
  } else if (filter !== 'all') {
    if (!s.enabled || s.status !== filter) return false
  }

  const needle = q.toLowerCase()
  if (!needle) return true
  return (
    s.name.toLowerCase().includes(needle) ||
    s.key.toLowerCase().includes(needle) ||
    s.target.toLowerCase().includes(needle) ||
    (s.description ?? '').toLowerCase().includes(needle) ||
    (s.tags ?? []).some((t) => t.toLowerCase().includes(needle))
  )
}

export function useServersPage() {
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

  const counts = useMemo(
    () => ({
      all: all.length,
      healthy: all.filter((s) => s.enabled && s.status === 'healthy').length,
      degraded: all.filter((s) => s.enabled && s.status === 'degraded').length,
      offline: all.filter((s) => s.enabled && s.status === 'offline').length,
      draining: all.filter((s) => s.draining).length,
      disabled: all.filter((s) => !s.enabled).length,
    }),
    [all],
  )

  const filterOptions: readonly FilterChipOption<ServerFilter>[] = [
    { value: 'all', label: '全部', count: counts.all },
    { value: 'healthy', label: '健康', tone: 'success', count: counts.healthy },
    { value: 'degraded', label: '降级', tone: 'warning', count: counts.degraded },
    { value: 'offline', label: '离线', tone: 'error', count: counts.offline },
    { value: 'draining', label: '停用中', tone: 'neutral', count: counts.draining },
    { value: 'disabled', label: '已停用', tone: 'neutral', count: counts.disabled },
  ]

  const filtered = useMemo(
    () => all.filter((s) => matchesServerFilter(s, state.filter, state.q)),
    [all, state.filter, state.q],
  )

  const sorted = useMemo(
    () => sortRows(filtered, serverSortKey(state.sort), state.dir),
    [filtered, state.sort, state.dir],
  )

  const page = paginate(sorted, state.page, state.pageSize)

  const onToggleEnabled = (s: ServerSummary, enabled: boolean) => {
    setEnabled.mutate(
      { id: s.id, enabled },
      {
        onSuccess: () =>
          toastSuccess(
            enabled ? '已启用' : '已停用',
            `${s.key} · ${enabled ? '开始接收流量' : '停止摘取新流量，存量调用会收尾'}`,
          ),
        onError: (e) => toastError(e, enabled ? '启用失败' : '停用失败'),
      },
    )
  }

  const onTest = (s: ServerSummary) => {
    testOne.mutate(s.id, {
      onSuccess: (probe) => {
        if (probe.ok) {
          toastSuccess('探活通过', `${s.key} · L1–L3 全部通过 · ${formatDuration(probe.totalMs)}`)
          return
        }
        toastError(new Error(probe.hint ?? probe.error ?? '探活失败'), `${s.key} 探活失败`)
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

  /** 搜索词或筛选条件非默认 —— 决定空态该说「没有匹配」还是「还没有」。 */
  const isFiltered = Boolean(state.q) || state.filter !== 'all'

  return {
    overview,
    list: { isLoading, error, refetch, total: data?.total ?? all.length },
    query: {
      state,
      /* URL 的 filter 是自由字符串，在此收窄成 ServerFilter，下游不必各自 cast。 */
      filter: state.filter as ServerFilter,
      setQ,
      setFilter,
      setPage,
      setPageSize,
      toggleSort,
      isFiltered,
    },
    filterOptions,
    counts,
    page,
    dialogs: {
      formOpen,
      setFormOpen,
      toolsTarget,
      setToolsTarget,
      credsTarget,
      setCredsTarget,
      deleteTarget,
      setDeleteTarget,
    },
    busy: {
      togglingId: setEnabled.isPending ? (setEnabled.variables?.id ?? null) : null,
      testingId: testOne.isPending ? (testOne.variables ?? null) : null,
      testAllPending: testAll.isPending,
      deleting: removeServer.isPending,
    },
    onToggleEnabled,
    onTest,
    onTestAll,
    onConfirmDelete,
  }
}
