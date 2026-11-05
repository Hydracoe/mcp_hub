import { Icon } from '@/components/common/Icon'
import { DotStat, MetricCard } from '@/components/common/KeyValue'
import { formatInt, formatPercent } from '@/lib/format'
import type { OverviewResponse } from '@/lib/api/types'

/** 顶部指标卡，全部取自 useOverview() 的返回值。 */
export function StatCards({ overview }: { overview: OverviewResponse }) {
  const { servers, tools, calls, credentials: keys } = overview

  const meetSla = calls.failureRate === undefined ? null : calls.failureRate < 0.02

  return (
    <div className="stat-grid">
      <MetricCard
        label="Server 健康"
        icon={<Icon name="activity" size={16} className="text-icon-muted" />}
        value={`${formatInt(servers.healthy)} / ${formatInt(servers.total)}`}
        suffix="健康"
        detail={
          <>
            <DotStat tone="success" label="healthy" value={formatInt(servers.healthy)} />
            <DotStat tone="warning" label="degraded" value={formatInt(servers.degraded)} />
            <DotStat tone="error" label="offline" value={formatInt(servers.offline)} />
            <DotStat tone="neutral" label="disabled" value={formatInt(servers.disabled)} />
          </>
        }
        footnote={`总数 ${formatInt(servers.total)} · ${formatInt(servers.degraded)} 台降级需关注 · 停用 ${formatInt(servers.disabled)} 台不计入健康`}
      />

      <MetricCard
        label="工具"
        icon={<Icon name="wrench" size={16} className="text-icon-muted" />}
        value={formatInt(tools.total)}
        suffix="启用中"
        detail={
          <>
            <span className="text-muted-foreground">
              下游声明 <span className="text-foreground">{formatInt(tools.cached ?? tools.total)}</span>
            </span>
            <span className="inline-flex items-center gap-1 rounded-md bg-fill-tertiary px-1.5 py-0.5 text-foreground">
              {formatInt(tools.hidden ?? 0)} 未启用
            </span>
          </>
        }
        footnote="启用中 = 此刻暴露给 AI 的工具；未启用 = 人工禁用，或所属 Server 已停用"
      />

      <MetricCard
        label="近 24h 调用"
        icon={<Icon name="trending-up" size={16} className="text-icon-muted" />}
        value={formatInt(calls.total)}
        detail={
          <>
            <span className="text-muted-foreground">
              失败 <span className="text-foreground">{formatInt(calls.failed)}</span>
            </span>
            <span>
              失败率{' '}
              <span
                className={
                  meetSla === false
                    ? 'font-semibold text-error-strong'
                    : meetSla === true
                      ? 'font-semibold text-success-strong'
                      : 'text-muted-foreground'
                }
              >
                {formatPercent(calls.failureRate)}
              </span>
            </span>
          </>
        }
        footnote={`SLA 目标 < 2% · ${meetSla === null ? '暂无数据' : meetSla ? '当前达标' : '当前超标'}`}
      />

      <MetricCard
        label="密钥池"
        icon={<Icon name="key-round" size={16} className="text-icon-muted" />}
        value={`可用 ${formatInt(keys.usable)}`}
        suffix={`/ ${formatInt(keys.total)}`}
        detail={
          <>
            <DotStat tone="success" label="usable" value={formatInt(keys.usable)} />
            <DotStat tone="warning" label="cooling" value={formatInt(keys.cooling)} />
            <DotStat tone="error" label="disabled" value={formatInt(keys.disabled)} />
          </>
        }
        footnote={`${formatInt(keys.unusable)} 把不可用，自愈中`}
      />
    </div>
  )
}
