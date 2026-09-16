import type { Dispatch, SetStateAction } from 'react'

import { Icon } from '@/components/common/Icon'
import { Label } from '@/components/ui/label'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { Switch } from '@/components/ui/switch'
import { Combobox } from '@/components/common/Combobox'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import type { AuthMode, Category, ServerDetail, ServerRequest, ToolFilterMode, Transport } from '@/lib/api/types'
import { transportLabel } from '@/lib/format'

import { TagInput, type KeyValueRow } from './components/FormBits'
import { TransportFields } from './components/TransportFields'

/* ───────────────────────── 表单状态（受控，自写校验） ───────────────────────── */

export interface ServerFormState {
  key: string
  name: string
  transport: Transport
  description: string
  tags: string[]
  categoryId: string
  enabled: boolean
  // stdio
  command: string
  argsText: string
  env: KeyValueRow[]
  // 远程
  url: string
  headers: KeyValueRow[]
  authMode: AuthMode
  authHeaderName: string
  // 高级
  timeoutMs: number
  protocolVersion: string
  legacyOnly: boolean
  toolFilterMode: ToolFilterMode
  toolFilterListText: string
  estTokensPerCall: number
}

const splitLines = (text: string) =>
  text
    .split('\n')
    .map((s) => s.trim())
    .filter(Boolean)

const parseEnv = (env: string[]): KeyValueRow[] =>
  env.map((e) => {
    const i = e.indexOf('=')
    return i < 0 ? { key: e, value: '' } : { key: e.slice(0, i), value: e.slice(i + 1) }
  })

const parseHeaders = (c: ServerDetail['config']): KeyValueRow[] => {
  if (c.headers && Object.keys(c.headers).length) {
    return Object.entries(c.headers).map(([key, value]) => ({ key, value }))
  }
  if (c.headerKeys) return c.headerKeys.map((key) => ({ key, value: '***' }))
  return []
}

export function createInitialForm(server: ServerDetail): ServerFormState {
  const c = server.config
  const headers = parseHeaders(c)
  return {
    key: server.key,
    name: server.name,
    transport: server.transport,
    description: server.description ?? '',
    tags: server.tags ?? [],
    categoryId: server.categoryId ?? '',
    enabled: server.enabled,
    command: c.command ?? '',
    argsText: (c.args ?? []).join('\n'),
    env: parseEnv(c.env ?? []),
    url: c.url ?? '',
    headers,
    authMode: c.authMode ?? server.authMode ?? 'none',
    authHeaderName: headers[0]?.key ?? 'Authorization',
    timeoutMs: c.timeoutMs,
    protocolVersion: c.protocolVersion ?? '',
    legacyOnly: c.legacyOnly,
    toolFilterMode: server.toolFilterMode,
    toolFilterListText: (server.toolFilterList ?? []).join('\n'),
    estTokensPerCall: c.estTokensPerCall ?? 0,
  }
}

/** 从表单状态组装请求体。注意 `***` 一律不回传（缺口 D6）。 */
export function buildServerRequest(s: ServerFormState): ServerRequest {
  const req: ServerRequest = {
    key: s.key,
    name: s.name,
    transport: s.transport,
    description: s.description.trim() || undefined,
    tags: s.tags.length ? s.tags : undefined,
    categoryId: s.categoryId || undefined,
    enabled: s.enabled,
    timeoutMs: s.timeoutMs,
    protocolVersion: s.protocolVersion.trim() || undefined,
    legacyOnly: s.legacyOnly,
    toolFilterMode: s.toolFilterMode,
    toolFilterList: s.toolFilterMode === 'allow_all' ? undefined : splitLines(s.toolFilterListText),
    estTokensPerCall: s.estTokensPerCall > 0 ? s.estTokensPerCall : undefined,
  }
  if (s.transport === 'stdio') {
    const args = splitLines(s.argsText)
    req.command = s.command.trim() || undefined
    req.args = args.length ? args : undefined
    req.env = s.env.filter((r) => r.key.trim()).map((r) => `${r.key.trim()}=${r.value}`)
  } else {
    req.url = s.url.trim() || undefined
    req.authMode = s.authMode
    if (s.authMode === 'header') {
      const headers: Record<string, string> = {}
      for (const r of s.headers) {
        // 回显值仍是 *** 说明未改，沿用原值：不能把 *** 当真值提交回去
        if (r.key.trim() && r.value !== '***') headers[r.key.trim()] = r.value
      }
      req.headers = headers
    }
  }
  return req
}

/* ───────────────────────── 表单主组件 ───────────────────────── */

export interface ServerFormProps {
  form: ServerFormState
  setForm: Dispatch<SetStateAction<ServerFormState>>
  categories: Category[]
}

const FILTER_HINT: Record<ToolFilterMode, string> = {
  allow_all: 'allow_all：放行全部工具，不过滤。',
  allow: 'allow 为白名单：仅暴露名单内的工具，其余被挡掉。空名单会被入口拦截（工具凭空消失）。',
  block: 'block 为黑名单：暴露除名单外全部工具，名单内的被挡掉。',
}

export function ServerForm({ form, setForm, categories }: ServerFormProps) {
  const update = (patch: Partial<ServerFormState>) => setForm((prev) => ({ ...prev, ...patch }))

  return (
    <div className="form-card">
      {/* 基础信息 */}
      <section className="form-section">
        <div className="form-section-header">基础信息</div>
        <div className="form-section-body space-y-4">
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
              {/* ⚠ 设计稿原版：Key 与 Name 同排且无「不可改」提示；此处按需求禁用并加 hint */}
              <Label htmlFor="sf-key">Key（唯一主键，不可修改）</Label>
              <Input
                id="sf-key"
                value={form.key}
                disabled
                className="font-mono"
                onChange={() => undefined}
              />
              <p className="form-hint">工具前缀，唯一约束。改名需另建 Server。</p>
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
              value={form.categoryId || '__none'}
              onChange={(v) => update({ categoryId: v === '__none' ? '' : v })}
              options={[
                { value: '__none', label: '未分类' },
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
            <Switch
              checked={form.enabled}
              onCheckedChange={(v) => update({ enabled: v })}
            />
          </div>
        </div>
      </section>

      {/* 传输配置 */}
      <section className="form-section">
        <div className="form-section-header">传输配置</div>
        <div className="form-section-body space-y-4">
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
        </div>
      </section>

      {/* 工具过滤 */}
      <section className="form-section">
        <div className="form-section-header">工具过滤</div>
        <div className="form-section-body space-y-4">
          <div>
            <Label>Filter Mode</Label>
            <Select
              value={form.toolFilterMode}
              onValueChange={(v) => update({ toolFilterMode: v as ToolFilterMode })}
            >
              <SelectTrigger className="w-full max-w-xs">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="allow_all">allow_all</SelectItem>
                <SelectItem value="allow">allow</SelectItem>
                <SelectItem value="block">block</SelectItem>
              </SelectContent>
            </Select>
          </div>
          {form.toolFilterMode !== 'allow_all' ? (
            <div>
              <Label htmlFor="sf-filter">Tool Name Patterns</Label>
              <Textarea
                id="sf-filter"
                rows={4}
                className="font-mono"
                value={form.toolFilterListText}
                onChange={(e) => update({ toolFilterListText: e.target.value })}
                placeholder="每行一个工具名，支持通配符"
              />
            </div>
          ) : null}
          <p className="form-hint">
            <Icon name="funnel" size={13} className="mr-1 inline-block align-[-2px]" />
            {FILTER_HINT[form.toolFilterMode]}
          </p>
        </div>
      </section>

      {/* TPM 预估 */}
      <section className="form-section">
        <div className="form-section-header">TPM 预估</div>
        <div className="form-section-body">
          <Label htmlFor="sf-tpm">estTokensPerCall</Label>
          <div className="relative max-w-xs">
            <Input
              id="sf-tpm"
              type="number"
              className="pr-16 font-mono"
              value={form.estTokensPerCall}
              onChange={(e) => update({ estTokensPerCall: Number(e.target.value) || 0 })}
            />
            <span className="pointer-events-none absolute right-3 top-1/2 -translate-y-1/2 text-[12px] text-muted-foreground">
              tokens
            </span>
          </div>
          {/* ⚠ 设计稿原版把 0 当真实值显示；此处 0 表示未计量 */}
          <p className="form-hint">
            {form.estTokensPerCall > 0
              ? '未计量 TPM 时用于三级回退估算'
              : '0 = 未计量，界面显示「未计量」而非 0'}
          </p>
        </div>
      </section>
    </div>
  )
}
