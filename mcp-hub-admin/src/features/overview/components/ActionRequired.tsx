import { Link } from 'react-router-dom'

import { Icon, type IconName } from '@/components/common/Icon'
import { Badge } from '@/components/ui/badge'
import { formatInt } from '@/lib/format'
import type { OverviewResponse } from '@/lib/api/types'

/** 一条待人工处理事项。tone 决定色彩：error 是「已经坏了」，
 *  warning 是「不改也能跑，但决定权在你」。 */
interface ActionItem {
  key: string
  icon: IconName
  tone: 'error' | 'warning'
  count: number
  title: string
  description: string
  to: string
  cta: string
}

const TONE_ICON = {
  error: 'bg-error-surface text-error-strong',
  warning: 'bg-warning-surface text-warning-strong',
} as const

/**
 * 待人工处理告警：只列网关无法自愈、不做决定就不会变好的事项。
 * 计数全部取自 overview 一处，不额外发请求，避免卡片与详情页对不上。
 */
export function ActionRequired({ overview }: { overview: OverviewResponse }) {
  const { servers, tools, credentials } = overview

  const items: ActionItem[] = []

  // 新工具默认禁用是刻意的安全默认，需人工确认后放行；这里显性提示并链到「待启用」筛选。
  const pending = tools.pendingEnable ?? 0
  if (pending > 0) {
    items.push({
      key: 'pending-tools',
      icon: 'wrench',
      tone: 'warning',
      count: pending,
      title: '新增工具待启用',
      description: '上游新出现的工具默认禁用，不会自动暴露给 AI。逐条确认后拨开开关即可放行。',
      to: '/tools?state=pending',
      cta: '去处理',
    })
  }

  // 重新启用 server 不会自动恢复被级联禁用的工具，这种「看起来开着、AI 侧却全空」的状态不会自愈。
  const enabledNoTools = servers.enabledNoTools ?? 0
  if (enabledNoTools > 0) {
    items.push({
      key: 'enabled-no-tools',
      icon: 'plug',
      tone: 'warning',
      count: enabledNoTools,
      title: 'Server 启用中但无生效工具',
      description: '停用期间工具被级联禁用，重新启用不会自动恢复。逐个启用工具，或直接停用 server。',
      to: '/servers',
      cta: '去处理',
    })
  }

  if (credentials.disabled > 0) {
    items.push({
      key: 'disabled-keys',
      icon: 'key-round',
      tone: 'error',
      count: credentials.disabled,
      title: '密钥已弃用',
      description: '弃用的密钥不会自动恢复（充值类会由探活自动救回，其余需人工确认后重新启用）。',
      to: '/credentials',
      cta: '去处理',
    })
  }

  if (servers.degraded > 0) {
    items.push({
      key: 'degraded-servers',
      icon: 'alert-triangle',
      tone: 'warning',
      count: servers.degraded,
      title: 'Server 降级',
      description: '整体失败保护已触发（多半是账号或配置问题），只在告警、未禁用密钥。',
      to: '/servers',
      cta: '去排查',
    })
  }

  if (servers.offline > 0) {
    items.push({
      key: 'offline-servers',
      icon: 'plug',
      tone: 'error',
      count: servers.offline,
      title: 'Server 离线',
      description: '探活连续失败，工具缓存保留但调用会快速失败。检查下游地址与凭据。',
      to: '/servers',
      cta: '去排查',
    })
  }

  if (items.length === 0) {
    return (
      <div className="ui-card flex flex-wrap items-center gap-x-2 gap-y-1 px-4 py-3">
        <Icon name="shield-check" size={15} className="text-success-strong" />
        <span className="text-[12.5px] font-medium text-foreground">暂无待人工处理事项</span>
      </div>
    )
  }

  return (
    <div className="ui-card overflow-hidden border-warning/30">
      <div className="flex flex-wrap items-center justify-between gap-2 border-b border-separator bg-warning-surface/50 px-4 py-3">
        <div className="flex items-center gap-2">
          <Icon name="triangle-alert" size={15} className="text-warning-strong" />
          <span className="text-[12.5px] font-semibold text-foreground">待人工处理</span>
          <Badge variant="warning">{items.length} 类 · {formatInt(items.reduce((s, i) => s + i.count, 0))} 项</Badge>
        </div>
        <span className="text-[11.5px] text-muted-foreground">这些事项不会自行恢复，需人工介入</span>
      </div>

      <div className="divide-y divide-separator">
        {items.map((item) => (
          <Link
            key={item.key}
            to={item.to}
            className="grid grid-cols-12 items-center gap-3 px-4 py-3 transition-colors hover:bg-fill-secondary/60"
          >
            <div className="col-span-8 flex min-w-0 items-start gap-2.5">
              <span
                className={`mt-0.5 grid size-6 shrink-0 place-items-center rounded-md ${TONE_ICON[item.tone]}`}
              >
                <Icon name={item.icon} size={13} />
              </span>
              <div className="min-w-0">
                <div className="text-[12.5px] font-medium text-foreground">{item.title}</div>
                <div className="text-[11.5px] leading-relaxed text-muted-foreground">
                  {item.description}
                </div>
              </div>
            </div>
            <div className="col-span-4 flex items-center justify-end gap-3">
              <span
                className={`font-mono text-[15px] tabular-nums ${item.tone === 'error' ? 'text-error-strong' : 'text-warning-strong'
                  }`}
              >
                {formatInt(item.count)}
              </span>
              <span className="inline-flex items-center gap-1 text-[11.5px] text-brand-strong">
                {item.cta}
                <Icon name="arrow-right" size={12} />
              </span>
            </div>
          </Link>
        ))}
      </div>
    </div>
  )
}
