import { useMemo } from 'react'
import { useSearchParams } from 'react-router-dom'

import type { ComboboxOption } from '@/components/common/Combobox'
import type { FilterChipOption } from '@/components/common/FilterChips'
import type { RailItem } from './components/CategoryRail'
import { useOverview, useTools } from '@/lib/api/queries'
import { formatInt } from '@/lib/format'
import type { ToolEntry } from '@/lib/api/types'

/** 启用状态：这个开关是升是降，比"下游通不通"更常被操作，所以是主筛选项。
 *  state 是 URL 参数名。 */
export type ToolState = 'all' | 'enabled' | 'disabled' | 'pending'

/** 下游可用性：网关这边配置都对、但下游此刻连不上时的排查入口。 */
export type ToolAvail = 'all' | 'available' | 'unavailable'

const PAGE_SIZE = 12

/** 分类条里「未分类」那一项的值。真实分类由用户在管理页命名，撞不上双下划线。 */
export const UNCATEGORIZED = '__none__'

/** 待启用是"未启用"里最有行动价值的一撮：它不是决定，是欠一个决定。 */
export const isPending = (t: ToolEntry): boolean => t.pending === true

/** enabled 缺省时当启用处理，宁可少报「未启用」。 */
export const isEnabled = (t: ToolEntry): boolean => t.enabled !== false

/** 按分类统计工具数，供左栏导航显示计数。 */
export const buildRailItems = (tools: readonly ToolEntry[]): RailItem[] => {
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
  if (uncategorized > 0) items.push({ value: UNCATEGORIZED, label: '未分类', count: uncategorized })
  return items
}

/** 取当前分类下的工具。后续所有计数与筛选都以它为基数。 */
export const toolsInCategory = (tools: readonly ToolEntry[], category: string): ToolEntry[] => {
  if (category === '') return [...tools]
  if (category === UNCATEGORIZED) return tools.filter((t) => !t.category)
  return tools.filter((t) => t.category === category)
}

export const matchesToolQuery = (
  t: ToolEntry,
  opts: { server: string; state: ToolState; avail: ToolAvail; q: string },
): boolean => {
  if (opts.server && t.serverKey !== opts.server) return false

  if (opts.state === 'enabled' && !isEnabled(t)) return false
  if (opts.state === 'disabled' && isEnabled(t)) return false
  if (opts.state === 'pending' && !isPending(t)) return false

  if (opts.avail === 'available' && !t.available) return false
  if (opts.avail === 'unavailable' && t.available) return false

  const needle = opts.q.trim().toLowerCase()
  if (!needle) return true
  return (
    t.exposedName.toLowerCase().includes(needle) ||
    t.name.toLowerCase().includes(needle) ||
    t.serverKey.toLowerCase().includes(needle) ||
    (t.category ?? '').toLowerCase().includes(needle) ||
    (t.description ?? '').toLowerCase().includes(needle)
  )
}

/** 工具管理页：列表含被禁用的工具，启停开关就架在这一份上。
 *  工具一次拉全量、前端筛选：分类条 / 条件 chip 的计数必须建立在全集之上。
 *  筛选状态全部挂 URL（与日志页同约定），默认值不写进 URL，避免空参数。 */
export function useToolsPage() {
  const { data: overview } = useOverview()
  const { data, isLoading, error, refetch } = useTools()
  const [params, setParams] = useSearchParams()

  /* 空串 = 不筛。分类栏的「全部」也用空串表达，URL 里干脆不出现。 */
  const category = params.get('category') ?? ''
  const rawServer = params.get('server') ?? ''
  const q = params.get('q') ?? ''
  const page = Math.max(1, Number(params.get('page')) || 1)
  /* URL 是可以手改的，非法值当默认处理，别让它渗进筛选逻辑。 */
  const stateParam = params.get('state')
  const state: ToolState =
    stateParam === 'enabled' || stateParam === 'disabled' || stateParam === 'pending'
      ? stateParam
      : 'all'
  const availParam = params.get('avail')
  const avail: ToolAvail =
    availParam === 'available' || availParam === 'unavailable' ? availParam : 'all'

  const tools = useMemo(() => data?.items ?? [], [data])

  /* server 参数既收 serverKey 也收 serverId。对外只认 key（稳定、可读），
   * 但旧链接、手敲 URL、从别处复制的 id 都得能用。
   * 认不出来的值原样保留：筛选结果为空，比悄悄把条件丢掉更诚实。 */
  const server = useMemo(() => {
    if (!rawServer) return ''
    for (const t of tools) {
      if (t.serverKey === rawServer) return rawServer
      if (t.serverId === rawServer) return t.serverKey
    }
    return rawServer
  }, [tools, rawServer])

  /* undefined / 空串 = 回到默认：把键从 URL 里删掉，而不是留一个空参数。 */
  const patch = (next: Record<string, string | number | undefined>) => {
    const p = new URLSearchParams(params)
    for (const [k, v] of Object.entries(next)) {
      if (v === undefined || v === '') p.delete(k)
      else p.set(k, String(v))
    }
    setParams(p, { replace: true })
  }

  /* 改条件时页码打回第 1 页，否则筛完停在旧页码会看到空列表。 */
  const setFilter = (next: Record<string, string | number | undefined>) =>
    patch({ ...next, page: undefined })

  const railItems = useMemo(() => buildRailItems(tools), [tools])

  /* 当前分类里的工具，二级条件的计数与筛选都以它为基数。 */
  const inCategory = useMemo(() => toolsInCategory(tools, category), [tools, category])

  const stateOptions = useMemo<readonly FilterChipOption<ToolState>[]>(() => {
    let enabled = 0
    let pending = 0
    for (const t of inCategory) {
      if (isEnabled(t)) enabled += 1
      else if (isPending(t)) pending += 1
    }
    return [
      { value: 'all', label: '全部', count: inCategory.length },
      { value: 'enabled', label: '已启用', tone: 'success', count: enabled },
      { value: 'disabled', label: '未启用', tone: 'warning', count: inCategory.length - enabled },
      { value: 'pending', label: '待启用', tone: 'error', count: pending },
    ]
  }, [inCategory])

  const availOptions = useMemo<readonly FilterChipOption<ToolAvail>[]>(() => {
    const available = inCategory.filter((t) => t.available).length
    return [
      { value: 'all', label: '全部', count: inCategory.length },
      { value: 'available', label: '可用', tone: 'success', count: available },
      { value: 'unavailable', label: '不可用', tone: 'error', count: inCategory.length - available },
    ]
  }, [inCategory])

  /* Server 数量不封顶，走带搜索的下拉；hint 显示工具数。 */
  const serverOptions = useMemo<ComboboxOption[]>(() => {
    const counts = new Map<string, number>()
    for (const t of inCategory) counts.set(t.serverKey, (counts.get(t.serverKey) ?? 0) + 1)
    return [
      { value: 'all', label: '全部 Server', hint: `${formatInt(inCategory.length)} 个工具` },
      ...[...counts.entries()]
        .sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))
        .map(([key, count]) => ({ value: key, label: key, hint: `${formatInt(count)} 个工具` })),
    ]
  }, [inCategory])

  const filtered = useMemo(
    () => inCategory.filter((t) => matchesToolQuery(t, { server, state, avail, q })),
    [inCategory, server, state, avail, q],
  )

  const pages = Math.max(1, Math.ceil(filtered.length / PAGE_SIZE))
  const current = Math.min(page, pages)
  const rows = filtered.slice((current - 1) * PAGE_SIZE, current * PAGE_SIZE)

  /* 分类也计入「筛选中」，清空才能回到全景。 */
  const hasFilter =
    category !== '' || server !== '' || state !== 'all' || avail !== 'all' || q.trim() !== ''

  const resetAll = () =>
    patch({
      category: undefined,
      server: undefined,
      state: undefined,
      avail: undefined,
      q: undefined,
      page: undefined,
    })

  return {
    overview,
    list: { isLoading, error, refetch },
    filters: { category, server, state, avail, q, page: current, hasFilter },
    railItems,
    stateOptions,
    availOptions,
    serverOptions,
    rows,
    filteredCount: filtered.length,
    pageSize: PAGE_SIZE,
    setFilter,
    patch,
    resetAll,
  }
}
