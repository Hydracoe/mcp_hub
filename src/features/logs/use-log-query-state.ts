import { useCallback, useMemo } from 'react'
import { useSearchParams } from 'react-router-dom'

import { DEFAULT_LOG_ORDER, DEFAULT_LOG_SORT, type LogOrder, type LogSort } from '@/lib/api/types'
import { useUiStore } from '@/stores/ui-store'

import {
  FIRST_DIR,
  LOG_FILTER_KEYS,
  readFilterValues,
  readOrder,
  readPage,
  readPageSize,
  readSort,
  toLogQuery,
} from './log-query'

/** 调用日志页的查询状态全部挂 URL：筛选结果可复制、可后退、刷新不丢，
 *  能直接发给同事 —— 这正是这一页的用法，内存 store 做不到。 */
export function useLogQueryState() {
  const uiPageSize = useUiStore((s) => s.pageSize)
  const [params, setParams] = useSearchParams()

  // 拆成局部变量而非 values 对象，否则 useMemo 依赖对象每次渲染换 identity 都重算。
  const { serverId, apiKeyId, status, kind, q, since, until } = readFilterValues(params)
  const page = readPage(params)
  const size = readPageSize(params, uiPageSize)
  const sort = readSort(params)
  const order = readOrder(params)

  const set = useCallback(
    (patch: Record<string, string | number | undefined>) => {
      setParams(
        (prev) => {
          const sp = new URLSearchParams(prev)
          for (const [k, v] of Object.entries(patch)) {
            if (v === undefined || v === '') sp.delete(k)
            else sp.set(k, String(v))
          }
          // 换筛选条件时回到第 1 页，避免停在不存在的页码
          if (Object.keys(patch).some((k) => (LOG_FILTER_KEYS as readonly string[]).includes(k))) {
            sp.delete('page')
          }
          return sp
        },
        { replace: true },
      )
    },
    [setParams],
  )

  /** 切排序：换列先给该列默认方向（FIRST_DIR），同列再点才翻转；排序变必回第 1 页。 */
  const toggleSort = useCallback(
    (col: LogSort) => {
      const nextDir: LogOrder = col === sort ? (order === 'asc' ? 'desc' : 'asc') : FIRST_DIR[col]
      set({
        // 默认值不写进 URL：长 URL 里塞满默认参数，分享出去没人看得懂哪个是重点
        sort: col === DEFAULT_LOG_SORT ? undefined : col,
        order: nextDir === DEFAULT_LOG_ORDER ? undefined : nextDir,
        page: 1,
      })
    },
    [order, set, sort],
  )

  const reset = useCallback(
    () =>
      set({
        serverId: undefined,
        apiKeyId: undefined,
        status: undefined,
        kind: undefined,
        q: undefined,
        since: undefined,
        until: undefined,
        sort: undefined,
        order: undefined,
      }),
    [set],
  )

  // query 挂 URL 便于把筛选结果发给同事；必须 memo，否则每次渲染换 identity 触发无限重拉。
  const query = useMemo(
    () =>
      toLogQuery({
        values: { serverId, apiKeyId, status, kind, q, since, until },
        sort,
        order,
        page,
        size,
      }),
    [serverId, apiKeyId, status, kind, q, since, until, sort, order, page, size],
  )

  return {
    values: { serverId, apiKeyId, status, kind, q, since, until },
    page,
    size,
    sort,
    order,
    /* `order` 是接口/URL 词，`dir` 是视图词，只在此行换算。 */
    dir: order,
    query,
    set,
    toggleSort,
    reset,
  }
}
