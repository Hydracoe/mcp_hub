import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'
import { Combobox } from '@/components/common/Combobox'

/**
 * 由 inputSchema 生成参数表单。
 *
 * 只覆盖 JSON Schema 里最常见的四类叶子：string / number / boolean / enum。
 * 数组、对象、oneOf、嵌套 properties 一律判定为「不支持」，
 * 由调用方退回原始 JSON 文本框 —— 写一个完整的 schema 解释器
 * 是在管理后台里投入产出比最低的事，而 schema 解释错了比让人自己写
 * JSON 更糟。
 */

export type FieldKind = 'string' | 'number' | 'boolean' | 'enum' | 'json'

export interface SchemaField {
  name: string
  kind: FieldKind
  required: boolean
  description?: string
  enumValues?: string[]
  placeholder?: string
}

export type Draft = Record<string, string | boolean>

export interface ParsedSchema {
  fields: SchemaField[]
  /** false 表示 schema 结构超出上面的覆盖面，调用方应退回 JSON 模式。 */
  supported: boolean
}

const LEAF = new Set(['string', 'number', 'integer', 'boolean'])

export function parseSchema(schema: Record<string, unknown> | undefined): ParsedSchema {
  if (!schema || typeof schema !== 'object') return { fields: [], supported: false }
  const props = schema.properties
  if (!props || typeof props !== 'object') {
    // 无参数工具是合法的：schema 里没有 properties，等于「调用时不需要参数」。
    const type = schema.type
    if (type === 'object' || type === undefined) return { fields: [], supported: true }
    return { fields: [], supported: false }
  }

  const requiredList = Array.isArray(schema.required) ? schema.required.map(String) : []
  const fields: SchemaField[] = []
  let supported = true

  for (const [name, raw] of Object.entries(props as Record<string, unknown>)) {
    if (!raw || typeof raw !== 'object') {
      supported = false
      continue
    }
    const def = raw as Record<string, unknown>
    const type = typeof def.type === 'string' ? def.type : undefined
    const enumValues = Array.isArray(def.enum) ? def.enum.map(String) : undefined
    const description = typeof def.description === 'string' ? def.description : undefined
    const required = requiredList.includes(name)

    if (enumValues) {
      fields.push({ name, kind: 'enum', required, description, enumValues })
    } else if (type && LEAF.has(type)) {
      const kind: FieldKind = type === 'boolean' ? 'boolean' : type === 'string' ? 'string' : 'number'
      const placeholder =
        typeof def.default !== 'undefined'
          ? `默认 ${String(def.default)}`
          : def.maximum !== undefined
            ? `最大 ${String(def.maximum)}`
            : undefined
      fields.push({ name, kind, required, description, placeholder })
    } else {
      // array / object / 无 type 的联合类型：表单表达不了，整份 schema 退回 JSON。
      supported = false
    }
  }

  return { fields, supported }
}

/** 用 schema 的 default 值预填草稿，省得每次都手打。 */
export function initialDraft(schema: Record<string, unknown> | undefined): Draft {
  const draft: Draft = {}
  const props = schema?.properties
  if (!props || typeof props !== 'object') return draft
  for (const [name, raw] of Object.entries(props as Record<string, unknown>)) {
    const def = (raw ?? {}) as Record<string, unknown>
    if (def.default === undefined) continue
    draft[name] = def.type === 'boolean' ? Boolean(def.default) : String(def.default)
  }
  return draft
}

export interface DraftResult {
  args: Record<string, unknown>
  /** 缺失的必填项名字。非空表示不能提交。 */
  missing: string[]
  /** 数字字段里填了非数字。 */
  invalid: string[]
}

export function draftToArgs(fields: SchemaField[], draft: Draft): DraftResult {
  const args: Record<string, unknown> = {}
  const missing: string[] = []
  const invalid: string[] = []

  for (const f of fields) {
    const raw = draft[f.name]
    if (f.kind === 'boolean') {
      args[f.name] = Boolean(raw)
      continue
    }
    const text = typeof raw === 'string' ? raw.trim() : ''
    if (text === '') {
      // 空值不塞进 args —— 下游对「未提供」和「给了空串」的处理不一样。
      if (f.required) missing.push(f.name)
      continue
    }
    if (f.kind === 'number') {
      const n = Number(text)
      if (Number.isNaN(n)) {
        invalid.push(f.name)
        continue
      }
      args[f.name] = n
      continue
    }
    args[f.name] = text
  }

  return { args, missing, invalid }
}

export function SchemaForm({
  fields,
  draft,
  onChange,
  idPrefix,
}: {
  fields: SchemaField[]
  draft: Draft
  onChange: (next: Draft) => void
  idPrefix: string
}) {
  return (
    <div className="space-y-3.5">
      {fields.map((f) => {
        const id = `${idPrefix}-${f.name}`
        const value = draft[f.name]
        return (
          <div key={f.name} className="space-y-1.5">
            <div className="flex items-center gap-2">
              <Label htmlFor={id} variant="plain" className="text-[12.5px] font-medium">
                <span className="font-mono">{f.name}</span>
              </Label>
              {f.required ? (
                <span className="text-[10.5px] text-error-strong">必填</span>
              ) : (
                <span className="text-[10.5px] text-muted-foreground">选填</span>
              )}
              <span className="font-mono text-[10.5px] text-icon-muted">{f.kind === 'enum' ? 'enum' : f.kind}</span>
            </div>

            {f.kind === 'boolean' ? (
              <div className="flex items-center gap-2">
                <Switch
                  id={id}
                  checked={Boolean(value)}
                  onCheckedChange={(v) => onChange({ ...draft, [f.name]: v })}
                />
                <span className="font-mono text-[12px] text-muted-foreground">
                  {Boolean(value) ? 'true' : 'false'}
                </span>
              </div>
            ) : f.kind === 'enum' ? (
              /* 下游 enum 长度不可控（国家码这类可能上百项），走带搜索的 Combobox */
              <Combobox
                id={id}
                value={typeof value === 'string' ? value : ''}
                onChange={(v) => onChange({ ...draft, [f.name]: v })}
                options={(f.enumValues ?? []).map((v) => ({ value: v, label: v }))}
                className="w-full"
                placeholder="选择一个值"
                searchPlaceholder="搜索取值…"
                emptyText="枚举里没有匹配值"
                aria-label={f.name}
              />
            ) : (
              <Input
                id={id}
                type={f.kind === 'number' ? 'number' : 'text'}
                inputMode={f.kind === 'number' ? 'decimal' : undefined}
                value={typeof value === 'string' ? value : ''}
                placeholder={f.placeholder}
                onChange={(e) => onChange({ ...draft, [f.name]: e.target.value })}
                className={f.kind === 'number' ? 'font-mono' : undefined}
              />
            )}

            {f.description ? <p className="form-hint">{f.description}</p> : null}
          </div>
        )
      })}
    </div>
  )
}

/** 原始 JSON 模式：schema 超出表单覆盖面时的退路。 */
export function RawArgsEditor({
  text,
  onChange,
  error,
}: {
  text: string
  onChange: (v: string) => void
  error?: string
}) {
  return (
    <div className="space-y-1.5">
      <Label htmlFor="smoke-args-raw" variant="plain" className="text-[12.5px] font-medium">
        Arguments JSON
      </Label>
      <Textarea
        id="smoke-args-raw"
        value={text}
        onChange={(e) => onChange(e.target.value)}
        rows={8}
        spellCheck={false}
        className="font-mono text-[12px]"
        aria-invalid={Boolean(error)}
      />
      {error ? <p className="form-hint text-error-strong">{error}</p> : <p className="form-hint">必须是一个 JSON 对象，如 {'{"query": "…"}'}</p>}
    </div>
  )
}
