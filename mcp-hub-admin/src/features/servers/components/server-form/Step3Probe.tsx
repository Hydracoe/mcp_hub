import { Switch } from '@/components/ui/switch'

import { Row } from './primitives'
import { ProbePanel } from './ProbePanel'
import {
  countSelectedTools,
  isAllToolsSelected,
  toCreateRequest,
  type ServerFormState,
} from './form-model'

interface Props {
  f: ServerFormState
  set: <K extends keyof ServerFormState>(k: K, v: ServerFormState[K]) => void
  onProbe: () => void
  sigStale: boolean
}

const AUTH_LABEL: Record<string, string> = {
  none: 'none',
  header: 'header',
  api_key: 'api_key',
}

export function Step3Probe({ f, set, onProbe, sigStale }: Props) {
  return (
    <>
      <ProbePanel f={f} set={set} onProbe={onProbe} sigStale={sigStale} />

      <Row label="创建后立即探活" hint="?probe=1 · 刷新网关的工具缓存，让勾选立即生效">
        <Switch checked={f.probe} onCheckedChange={(v) => set('probe', v)} />
      </Row>

      <SubmitSummary f={f} />
    </>
  )
}
/** 「将提交以下配置」快照。必须由 `toCreateRequest` 反推而不是重抄表单字段，
 *  否则显示的是用户填了什么，而非真正提交了什么。 */
function SubmitSummary({ f }: { f: ServerFormState }) {
  const body = toCreateRequest(f)
  const isStdio = f.transport === 'stdio'
  const selectedCount = countSelectedTools(f)
  const allSelected = isAllToolsSelected(f)

  const filterLabel = (() => {
    if (f.probeStatus === 'ok') {
      return allSelected ? '全部暴露（不过滤）' : `allow · 暴露 ${selectedCount}/${f.probeTools.length} 个`
    }
    if (f.fallbackAll) return '全部暴露（探活失败兜底）'
    return '待探活后选择'
  })()

  const rows: { label: string; value: string }[] = [
    { label: 'key', value: body.key || '—' },
    { label: 'name', value: body.name || '—' },
    { label: 'transport', value: f.transport },
    { label: 'target', value: (isStdio ? body.command : body.url) || '—' },
    { label: 'auth', value: isStdio ? 'stdio 环境变量' : AUTH_LABEL[body.authMode ?? 'none'] },
    { label: 'filter', value: filterLabel },
  ]

  return (
    <div className="rounded-lg border border-separator bg-fill-tertiary p-3 text-[12px] text-muted-foreground">
      <p className="mb-1 font-medium text-foreground">将提交以下配置：</p>
      {rows.map((r) => (
        <p key={r.label} className="font-mono">
          {r.label}: {r.value}
        </p>
      ))}
    </div>
  )
}
