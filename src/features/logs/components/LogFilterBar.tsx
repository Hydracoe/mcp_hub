import { useMemo } from 'react'

import { Combobox, type ComboboxOption } from '@/components/common/Combobox'
import { DateTimeRangePicker } from '@/components/common/DateTimeRangePicker'
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
import { SearchInput } from '@/components/common/SearchInput'
import { Switch } from '@/components/ui/switch'
import { ERROR_KINDS, LOG_CALLER_ANONYMOUS, type ApiKeyView, type ServerSummary } from '@/lib/api/types'
import { formatDateTime, formatRelative } from '@/lib/format'

import type { LogFilterValues } from '../log-query'

interface Props {
  servers: ServerSummary[]
  /** 用来把日志里的调用方 id 显示成人认得的名字。 */
  callers: ApiKeyView[]
  values: LogFilterValues
  set: (patch: Record<string, string | number | undefined>) => void
  reset: () => void
  onClear: () => void
  live: boolean
  onLiveChange: (v: boolean) => void
  lastRefresh?: number
}

export function LogFilterBar({
  servers,
  callers,
  values,
  set,
  reset,
  onClear,
  live,
  onLiveChange,
  lastRefresh,
}: Props) {
  const serverKey = servers.find((s) => s.id === values.serverId)?.key

  /* Server 与错误类型都是长清单，下拉里带搜索才扫得完。 */
  const serverOptions = useMemo<ComboboxOption[]>(
    () => [
      { value: 'all', label: '全部 Server' },
      ...servers.map((s) => ({ value: s.id, label: s.key, hint: s.name })),
    ],
    [servers],
  )

  const kindOptions = useMemo<ComboboxOption[]>(
    () => [
      { value: 'all', label: '全部类型' },
      ...ERROR_KINDS.map((k) => ({ value: k, label: k })),
    ],
    [],
  )

  /* 调用方选项用**当前**密钥清单命名，而不是日志里的名字快照 ——
     用户此刻选的是「哪一把密钥」，日志行显示的才是「当时叫什么」。 */
  const callerOptions = useMemo<ComboboxOption[]>(
    () => [
      { value: 'all', label: '全部调用方' },
      ...callers.map((k) => ({
        value: k.id,
        label: k.name,
        hint: k.enabled ? k.prefix : `${k.prefix} · 已停用`,
      })),
      { value: LOG_CALLER_ANONYMOUS, label: '匿名', hint: '端点未启用接入鉴权时的调用' },
    ],
    [callers],
  )

  const activeChips: FilterSummaryItem[] = []
  if (values.serverId) activeChips.push({ key: 'serverId', label: serverKey ?? values.serverId })
  if (values.apiKeyId) {
    const caller =
      values.apiKeyId === LOG_CALLER_ANONYMOUS
        ? '匿名'
        : (callers.find((k) => k.id === values.apiKeyId)?.name ?? values.apiKeyId)
    activeChips.push({ key: 'apiKeyId', label: `调用方 ${caller}` })
  }
  if (values.status) activeChips.push({ key: 'status', label: values.status })
  if (values.kind) activeChips.push({ key: 'kind', label: values.kind })
  if (values.q) activeChips.push({ key: 'q', label: `“${values.q}”` })
  if (values.since) activeChips.push({ key: 'since', label: `起 ${formatDateTime(values.since)}` })
  if (values.until) activeChips.push({ key: 'until', label: `止 ${formatDateTime(values.until)}` })

  return (
    <FilterPanel
      footer={
        <>
          <label className="inline-flex cursor-pointer items-center gap-2 text-xs text-muted-foreground">
            <Switch checked={live} onCheckedChange={onLiveChange} aria-label="实时刷新开关" />
            实时
          </label>
          <span className="text-xs text-muted-foreground">
            {live ? '每 5 秒自动刷新' : '已暂停刷新'}
            {lastRefresh ? ` · 上次刷新 ${formatRelative(lastRefresh)}` : ''}
          </span>
        </>
      }
      /* 清空不可逆，推到最右，与左侧可逆动作隔开。 */
      footerEnd={
        <button
          type="button"
          className="ui-btn ui-btn-destructive ui-btn-sm rounded-full"
          onClick={onClear}
        >
          <Icon name="trash-2" size={14} />
          清空日志
        </button>
      }
    >
      {/* 时间范围最宽，其余四列等宽。 */}
      <FilterGrid className="xl:grid-cols-[1fr_1fr_1fr_1fr_1.5fr]">
        <FilterField label="Server">
          <Combobox
            value={values.serverId ?? 'all'}
            onChange={(v) => set({ serverId: v === 'all' ? undefined : v })}
            options={serverOptions}
            className="w-full"
            placeholder="全部 Server"
            searchPlaceholder="搜索 key 或名称…"
            emptyText="没有匹配的 Server"
            aria-label="按 Server 筛选"
          />
        </FilterField>

        <FilterField label="调用方">
          <Combobox
            value={values.apiKeyId ?? 'all'}
            onChange={(v) => set({ apiKeyId: v === 'all' ? undefined : v })}
            options={callerOptions}
            className="w-full"
            placeholder="全部调用方"
            searchPlaceholder="搜索密钥名或前缀…"
            emptyText="没有匹配的调用方"
            aria-label="按调用方筛选"
          />
        </FilterField>

        <FilterField label="状态">
          {/* 三个值且带语义色，chips 比下拉好读；size 顶到 32px 与旁边的下拉对齐。 */}
          <FilterChips
            size="md"
            value={(values.status ?? 'all') as string}
            onChange={(v) => set({ status: v === 'all' ? undefined : (v as 'ok' | 'error') })}
            options={[
              { value: 'all', label: '全部' },
              { value: 'ok', label: 'ok', tone: 'success' },
              { value: 'error', label: 'error', tone: 'error' },
            ]}
          />
        </FilterField>

        <FilterField label="错误类型">
          <Combobox
            value={values.kind ?? 'all'}
            onChange={(v) => set({ kind: v === 'all' ? undefined : v })}
            options={kindOptions}
            className="w-full"
            placeholder="全部类型"
            searchPlaceholder="搜索错误类型…"
            emptyText="没有匹配的错误类型"
            aria-label="按错误类型筛选"
          />
        </FilterField>

        <FilterField label="时间范围">
          <DateTimeRangePicker
            since={values.since}
            until={values.until}
            onChange={({ since, until }) => set({ since, until })}
            className="w-full"
          />
        </FilterField>
      </FilterGrid>

      {/* 重置只清条件，是可逆动作，留在搜索框这一侧。 */}
      <FilterSearch>
        <SearchInput
          value={values.q}
          onChange={(q) => set({ q })}
          /* 参数与返回体不在搜索范围内，写进占位符免得用户以为搜错了。 */
          placeholder="按工具名 / Server / 调用方 / 下游密钥搜索…"
          className="flex-1"
          aria-label="日志搜索"
        />
        <button type="button" className="ui-btn ui-btn-ghost ui-btn-sm rounded-full" onClick={reset}>
          <Icon name="rotate-cw" size={14} />
          重置
        </button>
      </FilterSearch>

      <FilterSummary items={activeChips} onRemove={(key) => set({ [key]: undefined })} onClear={reset} />
    </FilterPanel>
  )
}
