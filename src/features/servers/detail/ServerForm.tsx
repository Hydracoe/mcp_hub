import type { Dispatch, SetStateAction } from 'react'

import { Combobox } from '@/components/common/Combobox'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'
import { transportLabel } from '@/lib/format'
import type { Category, Transport } from '@/lib/api/types'

import { TagInput } from './components/FormBits'
import { FormSection } from './components/FormSection'
import { TransportFields } from './components/TransportFields'
import { NO_CATEGORY, keyFormatError, type ServerFormState } from './server-form-model'

/** Server 详情页编辑表单。状态形状与「表单→请求体」换算在 server-form-model（被脏检查依赖），这里只渲染三个小节。 */

export interface ServerFormProps {
  form: ServerFormState
  setForm: Dispatch<SetStateAction<ServerFormState>>
  categories: Category[]
}

export function ServerForm({ form, setForm, categories }: ServerFormProps) {
  const update = (patch: Partial<ServerFormState>) => setForm((prev) => ({ ...prev, ...patch }))
  const keyError = keyFormatError(form.key)

  return (
    <div className="form-card">
      <FormSection title="基础信息">
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
          <div>
            <Label htmlFor="sf-name">Name</Label>
            <Input
              id="sf-name"
              value={form.name}
              onChange={(e) => update({ name: e.target.value })}
              placeholder="例如：Web Search API"
            />
          </div>
          <div>
            <Label htmlFor="sf-key">Key（工具前缀，唯一）</Label>
            <Input
              id="sf-key"
              value={form.key}
              className="font-mono"
              onChange={(e) => update({ key: e.target.value })}
              placeholder="例如：web-search"
              aria-invalid={Boolean(keyError)}
            />
            <p className="form-hint">
              工具前缀，唯一约束。改名会改掉已发布给 AI 的工具全名（key__tool），谨慎修改。
            </p>
            {keyError ? <p className="form-hint text-error-strong">{keyError}</p> : null}
          </div>
        </div>

        <div>
          <Label>Tags</Label>
          <TagInput value={form.tags} onChange={(tags) => update({ tags })} />
        </div>

        <div>
          <Label htmlFor="sf-desc">Description</Label>
          <Textarea
            id="sf-desc"
            rows={2}
            value={form.description}
            onChange={(e) => update({ description: e.target.value })}
            placeholder="一句话说明这个下游能做什么"
          />
        </div>

        <div>
          <Label>Category</Label>
          {/* 分类数随 Server 增长，走带搜索的 Combobox；无分类时留「未分类」兜底 */}
          <Combobox
            value={form.categoryId || NO_CATEGORY}
            onChange={(v) => update({ categoryId: v === NO_CATEGORY ? '' : v })}
            options={[
              { value: NO_CATEGORY, label: '未分类' },
              ...categories.map((c) => ({ value: c.id, label: c.name })),
            ]}
            className="mt-1.5 w-full max-w-xs"
            placeholder="未分类"
            searchPlaceholder="搜索分类…"
            emptyText="还没有分类"
            aria-label="Category"
          />
        </div>

        <div className="flex items-center justify-between">
          <div>
            <Label className="mb-0">Enabled</Label>
            <p className="form-hint">停用后从聚合列表移除，工具立即对 AI 客户端不可见</p>
          </div>
          <Switch checked={form.enabled} onCheckedChange={(v) => update({ enabled: v })} />
        </div>
      </FormSection>

      <FormSection title="传输配置">
        <div>
          <Label>Transport Type</Label>
          <Select value={form.transport} onValueChange={(v) => update({ transport: v as Transport })}>
            <SelectTrigger className="w-full max-w-xs">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="streamable_http">{transportLabel('streamable_http')}</SelectItem>
              <SelectItem value="sse">sse</SelectItem>
              <SelectItem value="stdio">stdio</SelectItem>
            </SelectContent>
          </Select>
          <p className="form-hint">切换类型会替换下方字段组</p>
        </div>

        <TransportFields form={form} setForm={setForm} />

        <div>
          <Label htmlFor="sf-timeout">Timeout</Label>
          <div className="relative max-w-xs">
            <Input
              id="sf-timeout"
              type="number"
              className="pr-12 font-mono"
              value={form.timeoutMs}
              onChange={(e) => update({ timeoutMs: Number(e.target.value) || 0 })}
            />
            <span className="pointer-events-none absolute right-3 top-1/2 -translate-y-1/2 text-[12px] text-muted-foreground">
              ms
            </span>
          </div>
          <p className="form-hint">默认 30s。单个 server 卡死不影响其他 server</p>
        </div>
      </FormSection>
    </div>
  )
}
