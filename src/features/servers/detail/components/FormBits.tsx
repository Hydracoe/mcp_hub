import { useState } from 'react'

import { Icon } from '@/components/common/Icon'
import { Input } from '@/components/ui/input'
import { cn } from '@/lib/utils'

/* ────────────────────────────── 标签 chip 输入 ────────────────────────────── */

export interface TagInputProps {
  value: string[]
  onChange: (next: string[]) => void
  placeholder?: string
}

/** 可增删的标签输入。 */
export function TagInput({ value, onChange, placeholder = '添加标签后回车' }: TagInputProps) {
  const [draft, setDraft] = useState('')

  const add = () => {
    const v = draft.trim()
    if (!v || value.includes(v)) {
      setDraft('')
      return
    }
    onChange([...value, v])
    setDraft('')
  }

  return (
    <div className="flex flex-wrap items-center gap-1.5 rounded-md border border-border bg-background p-1.5">
      {value.map((tag) => (
        <span key={tag} className="chip chip-quiet">
          {tag}
          <button
            type="button"
            aria-label={`移除标签 ${tag}`}
            className="ml-0.5 text-icon-muted hover:text-foreground"
            onClick={() => onChange(value.filter((t) => t !== tag))}
          >
            <Icon name="x" size={12} />
          </button>
        </span>
      ))}
      <input
        value={draft}
        onChange={(e) => setDraft(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === 'Enter') {
            e.preventDefault()
            add()
          }
        }}
        onBlur={add}
        placeholder={value.length ? '' : placeholder}
        className="min-w-[8ch] flex-1 bg-transparent px-1 py-0.5 text-[12.5px] text-foreground outline-none placeholder:text-muted-foreground"
      />
    </div>
  )
}

/* ────────────────────────────── 键值行编辑（Headers / Env） ────────────────────────────── */

export interface KeyValueRow {
  key: string
  value: string
}

export interface KeyValueEditorProps {
  rows: KeyValueRow[]
  onChange: (next: KeyValueRow[]) => void
  keyPlaceholder?: string
  valuePlaceholder?: string
  /** 值列等宽（密钥/header 场景）。 */
  mono?: boolean
  addLabel?: string
}

/** 键值对编辑器：远程 Headers 与 stdio Env 共用。回显值若仍是 *** 视为未改，保存不回传（见 buildServerRequest）。 */
export function KeyValueEditor({
  rows,
  onChange,
  keyPlaceholder = 'KEY',
  valuePlaceholder = 'value',
  mono = true,
  addLabel = '添加一项',
}: KeyValueEditorProps) {
  const set = (i: number, patch: Partial<KeyValueRow>) =>
    onChange(rows.map((r, idx) => (idx === i ? { ...r, ...patch } : r)))

  return (
    <div className="space-y-2">
      {rows.map((row, i) => (
        <div key={i} className="flex items-center gap-2">
          <Input
            value={row.key}
            onChange={(e) => set(i, { key: e.target.value })}
            placeholder={keyPlaceholder}
            className={cn('flex-1', mono && 'font-mono')}
          />
          <Input
            value={row.value}
            onChange={(e) => set(i, { value: e.target.value })}
            placeholder={valuePlaceholder}
            className={cn('flex-1', mono && 'font-mono')}
          />
          <button
            type="button"
            aria-label="删除该项"
            className="icon-btn shrink-0 text-icon-muted hover:text-error-strong"
            onClick={() => onChange(rows.filter((_, idx) => idx !== i))}
          >
            <Icon name="trash-2" size={15} />
          </button>
        </div>
      ))}
      <button
        type="button"
        onClick={() => onChange([...rows, { key: '', value: '' }])}
        className="flex h-9 w-full items-center justify-center gap-1.5 rounded-md border border-dashed border-border text-[12.5px] text-muted-foreground hover:text-foreground"
      >
        <Icon name="plus" size={14} />
        {addLabel}
      </button>
    </div>
  )
}
