import { Checkbox } from '@/components/ui/checkbox'

import type { ProbeTool } from './form-model'

interface Props {
  tools: readonly ProbeTool[]
  selected: Record<string, boolean>
  /** 暴露名前缀：`<serverKey>__<toolName>`，由工具名推导，让用户看清对外叫什么。 */
  keyPrefix: string
  onToggle: (name: string, checked: boolean) => void
  onToggleAll: (checked: boolean) => void
}

/** 下游工具勾选清单。工具名来自探活的 `tools/list`——
 *  创建时还不知道下游有什么工具，启用名单只能来自探活结果。 */
export function ToolPicker({ tools, selected, keyPrefix, onToggle, onToggleAll }: Props) {
  const selectedCount = tools.filter((t) => selected[t.name]).length
  const allSelected = tools.length > 0 && selectedCount === tools.length

  return (
    <div className="mt-3 overflow-hidden rounded-lg border border-separator">
      <div className="flex items-center gap-3 border-b border-separator bg-fill-tertiary px-3 py-2">
        <Checkbox
          checked={allSelected ? true : selectedCount > 0 ? 'indeterminate' : false}
          onCheckedChange={(v) => onToggleAll(v === true)}
          aria-label="全选 / 全不选"
        />
        <span className="text-[11px] font-semibold tracking-[0.06em] text-icon-muted uppercase">
          全选（{tools.length}）
        </span>
      </div>

      <div className="max-h-64 overflow-y-auto divide-y divide-separator">
        {tools.map((t) => (
          <label
            key={t.name}
            className="flex cursor-pointer items-start gap-3 px-3 py-2.5 transition-colors hover:bg-fill-secondary/60"
          >
            <Checkbox
              checked={Boolean(selected[t.name])}
              onCheckedChange={(v) => onToggle(t.name, v === true)}
              className="mt-0.5"
            />
            <div className="min-w-0 flex-1">
              <div className="truncate font-mono text-[12.5px] text-foreground" title={t.name}>
                {t.name}
              </div>
              {t.description ? (
                <div
                  className="mt-0.5 truncate text-[11.5px] text-muted-foreground"
                  title={t.description}
                >
                  {t.description}
                </div>
              ) : null}
            </div>
            <code className="mt-0.5 shrink-0 font-mono text-[11px] text-icon-muted">
              {keyPrefix}__{t.name}
            </code>
          </label>
        ))}
      </div>
    </div>
  )
}
