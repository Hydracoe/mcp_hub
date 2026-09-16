import { useEffect, useMemo, useRef, useState } from 'react'

import { CredentialStateBadge } from '@/components/common/StatusBadge'
import { EmptyState } from '@/components/common/States'
import { Icon } from '@/components/common/Icon'
import { formatCountdown, formatRelative } from '@/lib/format'
import type { KeyDetail } from '@/lib/api/types'

interface ServerGroup {
  serverKey: string
  items: KeyDetail[]
}

/** 展开后的单把密钥明细行。 */
function KeyRow({ detail, base }: { detail: KeyDetail; base: number }) {
  const isCooling = detail.state === 'cooling'
  // 倒计时按挂载时刻折算，配合父组件的 1s 重渲染真正在跳。
  const elapsed = Math.max(0, Date.now() - base)
  const remain = isCooling ? Math.max(0, (detail.coolingRemainingMs ?? 0) - elapsed) : 0

  return (
    <div className="grid grid-cols-12 items-center gap-3 px-4 py-2 text-[12px]">
      <div className="col-span-3 min-w-0 truncate text-muted-foreground" title={detail.label}>
        {detail.label}
      </div>
      <div className="col-span-2">
        <CredentialStateBadge state={detail.state} disabledKind={detail.disabledKind} />
      </div>
      <div className="col-span-3 min-w-0">
        {isCooling ? (
          <span className="font-mono text-warning-strong">{formatCountdown(remain)}</span>
        ) : (
          <span className="truncate text-muted-foreground">
            {detail.disabledKind ?? '—'}
            {detail.disabledNote ? ` · ${detail.disabledNote}` : ''}
          </span>
        )}
      </div>
      <div className="col-span-2 min-w-0 truncate font-mono text-muted-foreground" title={detail.lastError}>
        {detail.lastError ?? '—'}
      </div>
      <div className="col-span-2 text-right font-mono text-muted-foreground">
        {formatRelative(detail.lastUsedAt)}
      </div>
    </div>
  )
}

/**
 * 密钥池异常明细：只显示不可用（冷却 / 停用）的密钥。
 * 按 Server 分组，组行点击展开该 Server 下每把异常密钥的运行时明细。
 * keyDetails 为空 / undefined / 字段缺失时降级为正常态，不报错、不显示 NaN。
 */
export function KeyPoolSection({ details }: { details?: KeyDetail[] | null }) {
  // 1s 心跳：仅用于让冷却倒计时跳动，组件卸载时清掉。
  const [, setTick] = useState(0)
  const baseRef = useRef(Date.now())
  useEffect(() => {
    const id = window.setInterval(() => setTick((t) => t + 1), 1000)
    return () => window.clearInterval(id)
  }, [])

  const groups = useMemo(() => {
    const map = new Map<string, KeyDetail[]>()
    for (const d of details ?? []) {
      const bucket = map.get(d.serverKey)
      if (bucket) bucket.push(d)
      else map.set(d.serverKey, [d])
    }
    return [...map.entries()]
      .map<ServerGroup>(([serverKey, items]) => ({ serverKey, items }))
      .sort((a, b) => a.serverKey.localeCompare(b.serverKey))
  }, [details])

  /* 展开态：Record<serverKey, true>，数据刷新后已展开的组保持展开。 */
  const [expanded, setExpanded] = useState<Record<string, boolean>>({})
  const toggle = (serverKey: string) =>
    setExpanded((prev) => ({ ...prev, [serverKey]: !prev[serverKey] }))

  if (!details || details.length === 0) {
    return (
      <div className="ui-card">
        <EmptyState
          icon="shield-check"
          title="所有密钥均可正常调度"
          description="当前没有冷却或停用的密钥，密钥池处于健康状态。"
        />
      </div>
    )
  }

  return (
    <div className="ui-card overflow-hidden">
      <div className="flex items-center justify-between gap-3 px-4 py-3 border-b border-separator">
        <span className="text-[11.5px] text-muted-foreground">
          {groups.length} 个 Server 存在异常密钥 · 点击展开明细
        </span>
      </div>

      <div className="divide-y divide-separator">
        {groups.map(({ serverKey, items }) => {
          const open = Boolean(expanded[serverKey])
          const cooling = items.filter((d) => d.state === 'cooling').length
          const disabled = items.length - cooling

          return (
            <div key={serverKey}>
              <button
                type="button"
                onClick={() => toggle(serverKey)}
                className="grid w-full grid-cols-12 items-center gap-3 px-4 py-3 text-left transition-colors hover:bg-fill-secondary/60"
                aria-expanded={open}
              >
                <div className="col-span-7 flex min-w-0 items-center gap-2">
                  <Icon
                    name={open ? 'chevron-down' : 'chevron-right'}
                    size={14}
                    className="shrink-0 text-icon-muted"
                  />
                  <span className="truncate font-mono text-[12.5px] text-foreground" title={serverKey}>
                    {serverKey}
                  </span>
                </div>
                <div className="col-span-5 flex items-center justify-end gap-2 text-[11px] tabular-nums">
                  {cooling > 0 ? (
                    <span className="rounded-full bg-warning-surface px-2 py-0.5 text-warning-strong">
                      冷却 {cooling}
                    </span>
                  ) : null}
                  {disabled > 0 ? (
                    <span className="rounded-full bg-error-surface px-2 py-0.5 text-error-strong">
                      停用 {disabled}
                    </span>
                  ) : null}
                  <span className="text-muted-foreground">共 {items.length} 把异常</span>
                </div>
              </button>

              {open ? (
                <div className="border-t border-separator bg-fill-secondary/40 pb-2">
                  <div className="grid grid-cols-12 gap-3 px-4 py-1.5 text-[10.5px] font-semibold uppercase tracking-[0.06em] text-icon-muted">
                    <div className="col-span-3">密钥</div>
                    <div className="col-span-2">状态</div>
                    <div className="col-span-3">冷却剩余 / 停用说明</div>
                    <div className="col-span-2">最近错误</div>
                    <div className="col-span-2 text-right">最近使用</div>
                  </div>
                  {items.map((d) => (
                    <KeyRow key={d.credentialId} detail={d} base={baseRef.current} />
                  ))}
                </div>
              ) : null}
            </div>
          )
        })}
      </div>
    </div>
  )
}
