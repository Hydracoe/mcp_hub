import { Icon } from '@/components/common/Icon'
import { DotStat, MetricCard } from '@/components/common/KeyValue'
import { formatInt, formatPercent } from '@/lib/format'
import type { OverviewResponse } from '@/lib/api/types'

/** 顶部指标卡区。四张卡全部取自 useOverview() 的返回值
 *  （ServerStats / ToolStats / CallStats / KeySummary），不用写死的假数字。 */
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
          </>
        }
        footnote={`总数 ${formatInt(servers.total)} · ${formatInt(servers.degraded)} 台降级需关注`}
      />

      <MetricCard
        label="工具总数"
        icon={<Icon name="wrench" size={16} className="text-icon-muted" />}
        value={formatInt(tools.total)}
        suffix={`/ ${formatInt(tools.cached ?? tools.total)}`}
        detail={
          <>
            <span className="text-muted-foreground">
              下游声明 <span className="text-foreground">{formatInt(tools.cached ?? tools.total)}</span>
            </span>
            <span className="text-foreground">生效中 {formatInt(tools.total)}</span>
            <span className="inline-flex items-center gap-1 rounded-md bg-fill-tertiary px-1.5 py-0.5 text-foreground">
              {formatInt(tools.hidden ?? 0)} 未启用
            </span>
          </>
        }
        footnote="未启用 = 人工禁用，或 Server 未探活 / 已停用"
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
