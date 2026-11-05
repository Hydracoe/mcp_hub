import { useCallback, useState } from 'react'

import { useApiKeys, useClearLogs, useLogs, useOverview, useServers } from '@/lib/api/queries'
import { toastSuccess } from '@/lib/toast'
import type { LogItem } from '@/lib/api/types'

import { useLogQueryState } from './use-log-query-state'

/** 调用日志页的状态机：URL 查询状态、轮询开关、详情抽屉与清空确认的开合；页面只负责渲染。 */
export function useLogsPage() {
  const { data: overview } = useOverview()
  const { data: servers } = useServers()
  /* 调用方筛选项来自接入密钥清单：日志只存 id/名字快照，需清单给出可读名字。 */
  const { data: keys } = useApiKeys()

  const filters = useLogQueryState()

  const [tab, setTab] = useState<'logs' | 'keys'>('logs')
  const [live, setLive] = useState(true)
  const [selected, setSelected] = useState<LogItem | null>(null)
  const [sheetOpen, setSheetOpen] = useState(false)
  const [clearOpen, setClearOpen] = useState(false)

  // 抽屉 / 弹框打开时自动暂停轮询，避免内容在阅读时跳动。
  const polling = live && !sheetOpen && !clearOpen
  const logs = useLogs(filters.query, polling)
  const clear = useClearLogs()

  const items = logs.data?.items ?? []
  const total = logs.data?.total ?? 0

  const openDetail = useCallback((log: LogItem) => {
    setSelected(log)
    setSheetOpen(true)
  }, [])

  const onConfirmClear = () => {
    clear.mutate(undefined, {
      onSuccess: () => {
        toastSuccess('日志已清空', '所有调用日志已被删除')
        setClearOpen(false)
      },
      onError: () => setClearOpen(false),
    })
  }

  /* 仅「开着实时却被弹层挡住」时提示；手动关实时不提示，避免与开关旁状态重复。 */
  const pausedByOverlay = !polling && live

  return {
    overview,
    logs: {
      items,
      total,
      error: logs.error,
      loading: logs.isLoading && !logs.data,
      refetch: logs.refetch,
      dataUpdatedAt: logs.dataUpdatedAt,
      /** 轮询开着但被弹层挡住。密钥事件表也用它决定要不要跟着刷。 */
      polling,
      pausedByOverlay,
      pausedReason: sheetOpen ? '查看详情时暂停刷新' : '确认操作时暂停刷新',
    },
    servers: servers?.items ?? [],
    callers: keys?.items ?? [],
    filters,
    tab,
    setTab,
    live,
    setLive,
    detail: { selected, open: sheetOpen, setOpen: setSheetOpen },
    clearDialog: { open: clearOpen, setOpen: setClearOpen, pending: clear.isPending },
    openDetail,
    onConfirmClear,
  }
}
