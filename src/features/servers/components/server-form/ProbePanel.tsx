import { Button } from '@/components/ui/button'
import { Icon } from '@/components/common/Icon'
import { Switch } from '@/components/ui/switch'

import { Row } from './primitives'
import { ToolPicker } from './ToolPicker'
import { countSelectedTools, type ServerFormState } from './form-model'

interface Props {
  f: ServerFormState
  set: <K extends keyof ServerFormState>(k: K, v: ServerFormState[K]) => void
  onProbe: () => void
  /** 探活成功后连接配置又被改过：旧清单已作废，要盖回「未探活」态。 */
  sigStale: boolean
}

/** 探活状态机的外观：未探活 / 探活中 / 成功（含勾选）/ 失败（含兜底开关），
 *  四态各自提前 return。 */
export function ProbePanel({ f, set, onProbe, sigStale }: Props) {
  const selectedCount = countSelectedTools(f)

  if (f.probeStatus === 'idle' || sigStale) {
    return (
      <div className="rounded-lg border border-separator bg-fill-tertiary p-4">
        <p className="text-[12.5px] text-foreground">
          {sigStale
            ? '连接配置在上次探活后发生了变化，旧的工具清单已作废。'
            : '先探活验证连通性，探活成功后列出下游工具，由你勾选哪些启用。'}
        </p>
        <Button variant="primary" size="sm" className="mt-3" onClick={onProbe}>
          <Icon name="radio" size={14} /> 开始探活
        </Button>
      </div>
    )
  }

  if (f.probeStatus === 'probing') {
    return (
      <div className="flex items-center gap-2 rounded-lg border border-separator bg-fill-tertiary p-4 text-[12.5px] text-muted-foreground">
        <Icon name="refresh-cw" size={14} className="animate-spin" />
        正在探活（L1 可达性 → L2 协议协商 → L3 工具清单）…
      </div>
    )
  }

  if (f.probeStatus === 'ok') {
    return (
      <div className="rounded-lg border border-separator p-4">
        <div className="flex flex-wrap items-center gap-2 text-[12px]">
          <Icon name="circle-check" size={15} className="text-success-strong" />
          <span className="font-medium text-foreground">探活成功</span>
          <span className="text-muted-foreground">
            共 {f.probeTools.length} 个工具 · 已选 {selectedCount} 个
            {selectedCount < f.probeTools.length ? '，未选的不会对外暴露' : ''}
          </span>
          <Button variant="ghost" size="sm" className="ml-auto" onClick={onProbe}>
            <Icon name="refresh-cw" size={13} /> 重新探活
          </Button>
        </div>

        <ToolPicker
          tools={f.probeTools}
          selected={f.probeSelected}
          keyPrefix={f.key.trim() || 'key'}
          onToggle={(name, checked) =>
            set('probeSelected', { ...f.probeSelected, [name]: checked })
          }
          onToggleAll={(checked) =>
            set(
              'probeSelected',
              Object.fromEntries(f.probeTools.map((t) => [t.name, checked])),
            )
          }
        />
      </div>
    )
  }

  return (
    <div className="rounded-lg border border-separator bg-error-surface/60 p-4">
      <div className="flex items-center gap-2 text-[12.5px]">
        <Icon name="x-circle" size={15} className="text-error-strong" />
        <span className="font-medium text-foreground">探活失败</span>
        {f.probeFail?.kind ? (
          <code className="font-mono text-[11px] text-error-strong">{f.probeFail.kind}</code>
        ) : null}
      </div>

      {f.probeFail?.hint ? (
        <p className="mt-1.5 text-[12px] text-foreground">{f.probeFail.hint}</p>
      ) : null}
      {f.probeFail?.detail ? (
        <p
          className="mt-1 truncate font-mono text-[11.5px] text-muted-foreground"
          title={f.probeFail.detail}
        >
          {f.probeFail.detail}
        </p>
      ) : null}
      {f.probeFail?.stderr ? (
        <pre className="mt-2 max-h-28 overflow-y-auto rounded-md bg-fill-tertiary p-2 font-mono text-[11px] text-muted-foreground">
          {f.probeFail.stderr}
        </pre>
      ) : null}

      <div className="mt-3 flex items-center gap-2">
        <Button variant="outline" size="sm" onClick={onProbe}>
          <Icon name="refresh-cw" size={13} /> 重新探活
        </Button>
      </div>

      <Row
        label="跳过筛选，暴露全部"
        hint="探不上的下游也可以先创建（记为 offline），之后在工具页再筛"
      >
        <Switch checked={f.fallbackAll} onCheckedChange={(v) => set('fallbackAll', v)} />
      </Row>
    </div>
  )
}
