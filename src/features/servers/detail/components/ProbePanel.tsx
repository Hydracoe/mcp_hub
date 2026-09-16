import { Icon } from '@/components/common/Icon'
import { Badge } from '@/components/ui/badge'
import { ErrorKindBadge } from '@/components/common/StatusBadge'
import { Spinner } from '@/components/common/States'
import { formatDuration } from '@/lib/format'
import { cn } from '@/lib/utils'
import type { ProbeLevel, ProbeResult } from '@/lib/api/types'

export interface ProbePanelProps {
  probe?: ProbeResult
  testing: boolean
  onTest: () => void
}

function LevelRow({ level }: { level: ProbeLevel }) {
  const ok = level.ok
  return (
    <div className="probe-level">
      <Icon
        name={ok ? 'circle-check' : 'x-circle'}
        size={18}
        className={cn('shrink-0', ok ? 'text-success-strong' : 'text-error-strong')}
      />
      <div className="min-w-0 flex-1">
        <div className="flex items-center justify-between gap-2">
          <span className="text-[13px] font-medium text-foreground">{level.label}</span>
          <span
            className={cn(
              'shrink-0 font-mono text-[12px] tabular-nums',
              ok ? 'text-success-strong' : 'text-error-strong',
            )}
          >
            {formatDuration(level.durationMs)}
          </span>
        </div>
        {level.detail ? (
          <p className="mt-0.5 truncate text-[11.5px] text-muted-foreground" title={level.detail}>
            {level.detail}
          </p>
        ) : null}
        {level.errorKind ? (
          <div className="mt-1">
            <ErrorKindBadge kind={level.errorKind} />
          </div>
        ) : null}
        {/* 失败 stderr 单独回显，便于定位 stdio 启动问题 */}
        {level.stderr ? (
          <pre className="code-block mt-1.5 max-h-40 overflow-auto whitespace-pre-wrap p-2 text-[11px] leading-relaxed">
            {level.stderr}
          </pre>
        ) : null}
      </div>
    </div>
  )
}

export function ProbePanel({ probe, testing, onTest }: ProbePanelProps) {
  return (
    <div className="form-card">
      <div className="form-section-header flex items-center justify-between">
        <span>连通性测试</span>
        <button type="button" className="ui-btn ui-btn-ghost h-7 px-2.5 text-[12px]" onClick={onTest} disabled={testing}>
          {testing ? <Spinner size={13} /> : <Icon name="activity" size={14} />}
          测试
        </button>
      </div>
      <div className="form-section-body" style={{ paddingTop: 0, paddingBottom: 0 }}>
        {!probe ? (
          <div className="flex items-center gap-2 py-4 text-[12.5px] text-muted-foreground">
            <Icon name="circle-minus" size={16} />
            尚未探活。点击「测试」运行 L1→L3 探活。
          </div>
        ) : (
          <>
            <div className="flex items-center justify-between gap-2 py-2.5">
              <Badge variant={probe.ok ? 'success' : 'error'}>{probe.ok ? '通过' : '失败'}</Badge>
              <span className="font-mono text-[12px] tabular-nums text-muted-foreground">
                总耗时 {formatDuration(probe.totalMs)}
              </span>
            </div>
            <div className="grid grid-cols-2 gap-x-3 gap-y-1 border-t border-separator py-2.5 text-[11.5px]">
              <Meta label="协议版本" value={probe.protocolVersion ?? '—'} mono />
              <Meta label="协议时代" value={probe.protocolEra ?? '—'} mono />
              <Meta label="server" value={probe.serverName ?? '—'} mono />
              <Meta label="版本" value={probe.serverVersion ?? '—'} mono />
              <Meta label="工具数" value={probe.toolCount != null ? String(probe.toolCount) : '—'} mono />
              <Meta
                label="能力"
                value={probe.capabilities?.length ? probe.capabilities.join(', ') : '—'}
                mono
              />
            </div>
            <div className="divide-y divide-separator">
              {/* 老响应可能没有 levels：缺省当「无分段明细」渲染，不能崩。 */}
              {(probe.levels ?? []).map((lvl) => (
                <LevelRow key={lvl.level} level={lvl} />
              ))}
            </div>
            {probe.error || probe.hint ? (
              <div className="border-t border-separator py-2.5 text-[11.5px]">
                {probe.error ? <p className="font-mono text-error-strong">{probe.error}</p> : null}
                {probe.hint ? <p className="mt-1 text-muted-foreground">{probe.hint}</p> : null}
              </div>
            ) : null}
          </>
        )}
      </div>
    </div>
  )
}

function Meta({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="flex items-baseline gap-1.5">
      <span className="shrink-0 text-icon-muted">{label}</span>
      <span className={cn('min-w-0 truncate text-foreground', mono && 'font-mono')}>{value}</span>
    </div>
  )
}
