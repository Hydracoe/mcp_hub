import { useCallback, useMemo } from 'react'
import { useSearchParams } from 'react-router-dom'

/** 列表查询状态（搜索/筛选/排序/分页）挂在 URL 上：状态要能被复制、后退、刷新不丢；
 *  放全局 store 会让用户跳回时带着自己不记得的筛选。纯函数部分在同级的 `../list-utils`。 */
export interface ListQueryState {
  q: string
  /** 状态 / 分类这类单值筛选。 */
  filter: string
  sort: string
  /** 排序方向。1 升序，-1 降序。 */
  dir: 1 | -1
  page: number
  pageSize: number
}

export interface UseListQueryStateOptions {
  defaultSort?: string
  defaultDir?: 1 | -1
  defaultFilter?: string
  defaultPageSize?: number
  /** 排序字段的合法值；非法值会被忽略，避免 URL 手改成脏字段。 */
  sortKeys: readonly string[]
}

export function useListQueryState({
  defaultSort,
  defaultDir = 1,
  defaultFilter = 'all',
  defaultPageSize = 20,
  sortKeys,
}: UseListQueryStateOptions) {
  const [params, setParams] = useSearchParams()

  const state = useMemo<ListQueryState>(() => {
    const sort = params.get('sort') ?? defaultSort ?? ''
    const rawDir = params.get('dir')
    return {
      q: params.get('q') ?? '',
      filter: params.get('filter') ?? defaultFilter,
      sort: sortKeys.includes(sort) ? sort : (defaultSort ?? ''),
      dir: rawDir === 'desc' || rawDir === '-1' ? -1 : rawDir === 'asc' ? 1 : defaultDir,
      page: Math.max(1, Number(params.get('page') ?? 1) || 1),
      pageSize: Math.max(1, Number(params.get('size') ?? defaultPageSize) || defaultPageSize),
    }
  }, [params, defaultSort, defaultDir, defaultFilter, defaultPageSize, sortKeys])

  const patch = useCallback(
    (next: Partial<Record<'q' | 'filter' | 'sort' | 'page' | 'size' | 'dir', string | number>>) => {
      setParams(
        (prev) => {
          const sp = new URLSearchParams(prev)
          for (const [k, v] of Object.entries(next)) {
            if (v === '' || v === undefined || v === null) sp.delete(k)
            else sp.set(k, String(v))
          }
          return sp
        },
        // 筛选类变更不该往历史栈里塞一堆条目，replace 更贴近直觉。
        { replace: true },
      )
    },
    [setParams],
  )

  /** 换筛选/搜索时必须回到第 1 页 —— 否则会停在一个不存在的页码上。 */
  const setQ = useCallback((q: string) => patch({ q, page: 1 }), [patch])
  const setFilter = useCallback((filter: string) => patch({ filter, page: 1 }), [patch])
  const setPage = useCallback((page: number) => patch({ page }), [patch])
  const setPageSize = useCallback((size: number) => patch({ size, page: 1 }), [patch])
  const toggleSort = useCallback(
    (key: string) => {
      if (state.sort === key) {
        patch({ dir: state.dir === 1 ? -1 : 1, page: 1 })
      } else {
        patch({ sort: key, dir: 1, page: 1 })
      }
    },
    [patch, state.sort, state.dir],
  )

  return { state, setQ, setFilter, setPage, setPageSize, toggleSort, patch }
}
