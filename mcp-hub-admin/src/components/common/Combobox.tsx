import { useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { Command, CommandList, CommandItem } from 'cmdk'
import { CheckIcon, ChevronDownIcon, SearchIcon } from 'lucide-react'

import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { cn } from '@/lib/utils'

/** 一项。label 是主文本，hint 是右侧灰字（server 的展示名、分类的英文名）。 */
export interface ComboboxOption {
  value: string
  label: string
  hint?: string
  /** label 与 hint 之外的额外匹配文本（别名、工具名片段）。 */
  keywords?: string
  disabled?: boolean
  /** 分组标题。同组会被收在一个标题下，顺序按传入顺序。 */
  group?: string
}

export interface ComboboxProps {
  value?: string | null
  onChange: (value: string) => void
  options: readonly ComboboxOption[]
  placeholder?: string
  searchPlaceholder?: string
  emptyText?: string
  /** 触发器宽度与外形：表单里传 `w-full`，工具条里给定宽。 */
  className?: string
  size?: 'sm' | 'default'
  disabled?: boolean
  /** 触发器右侧的额外内容（如计数徽标）。 */
  triggerExtra?: ReactNode
  /** 菜单最大高度，默认 288px。 */
  maxHeight?: number
  /** 值不在 options 里时的显示文本（值还没加载出来时）。 */
  fallbackLabel?: string
  /** 触发器 id，给 `<Label htmlFor>` 用。 */
  id?: string
  'aria-label'?: string
}

function matches(o: ComboboxOption, needle: string): boolean {
  if (!needle) return true
  return (
    o.label.toLowerCase().includes(needle) ||
    (o.hint?.toLowerCase().includes(needle) ?? false) ||
    (o.keywords?.toLowerCase().includes(needle) ?? false)
  )
}

/** 带搜索的下拉，用于选项多于 8 个的场景；短列表请用 `@/components/ui/select`。
 *  用 Popover + cmdk 而非 Radix Select：后者是「选中即提交」，硬塞输入框会跟
 *  它自带的「输入字母跳选项」打架。
 *  过滤自己实现（cmdk 传 shouldFilter={false}），因为要匹配 hint 与 keywords。 */
export function Combobox({
  value,
  onChange,
  options,
  placeholder = '请选择',
  searchPlaceholder = '搜索…',
  emptyText = '没有匹配项',
  className,
  size = 'default',
  disabled = false,
  triggerExtra,
  maxHeight = 288,
  fallbackLabel,
  id,
  'aria-label': ariaLabel,
}: ComboboxProps) {
  const [open, setOpen] = useState(false)
  const [query, setQuery] = useState('')
  /* cmdk 的「当前高亮项」。受控之后才能做到：一打开就高亮选中项并滚到它。 */
  const [active, setActive] = useState('')
  const listRef = useRef<HTMLDivElement>(null)
  const inputRef = useRef<HTMLInputElement>(null)

  const selected = useMemo(() => options.find((o) => o.value === value), [options, value])

  const filtered = useMemo(() => {
    const needle = query.trim().toLowerCase()
    return needle ? options.filter((o) => matches(o, needle)) : options
  }, [options, query])

  /* 分组保持传入顺序 —— 调用方通常已经按「常用优先」排过。 */
  const groups = useMemo(() => {
    const map = new Map<string, ComboboxOption[]>()
    for (const o of filtered) {
      const key = o.group ?? ''
      const list = map.get(key)
      if (list) list.push(o)
      else map.set(key, [o])
    }
    return [...map.entries()]
  }, [filtered])

  /* 每次打开重置搜索词、高亮回到选中项，并手动给焦点（portal 里 autoFocus 不可靠）。 */
  useEffect(() => {
    if (!open) return
    setQuery('')
    const first = filtered[0]
    setActive(value && filtered.some((o) => o.value === value) ? value : (first?.value ?? ''))
    inputRef.current?.focus()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open])

  /* 过滤后高亮项消失了就把高亮挪到第一项，避免回车时选中一个看不见的项。 */
  useEffect(() => {
    if (!open) return
    if (filtered.some((o) => o.value === active)) return
    setActive(filtered[0]?.value ?? '')
  }, [filtered, active, open])

  const pick = (next: string) => {
    onChange(next)
    setOpen(false)
  }

  return (
    <Popover open={open} onOpenChange={(o) => !disabled && setOpen(o)}>
      <PopoverTrigger asChild>
        <button
          type="button"
          id={id}
          role="combobox"
          aria-expanded={open}
          aria-label={ariaLabel}
          disabled={disabled}
          data-size={size}
          /* data-placeholder 让 CSS 把占位态染成灰字，与 Select 一致 */
          data-placeholder={selected ? undefined : ''}
          className={cn('ui-select-trigger group flex items-center justify-between gap-2', className)}
        >
          <span className="flex min-w-0 flex-1 items-center gap-2">
            <span className="truncate">{selected ? selected.label : (fallbackLabel ?? placeholder)}</span>
            {selected?.hint ? (
              <span className="shrink-0 truncate text-[12px] text-muted-foreground">{selected.hint}</span>
            ) : null}
          </span>
          {triggerExtra}
          <ChevronDownIcon className="size-4 shrink-0 text-icon-muted transition-transform duration-200 group-data-[state=open]:rotate-180" />
        </button>
      </PopoverTrigger>

      <PopoverContent
        align="start"
        /* 与触发器同宽（窄工具条里 190px 也不会挤成一坨），下限 224px */
        className="ui-popover w-(--radix-popover-trigger-width) min-w-56 overflow-hidden p-0"
        /* 焦点交给下面那个输入框，不要让面板自己抢 */
        onOpenAutoFocus={(e) => e.preventDefault()}
      >
        {/* 搜索框与列表必须放在 Command 根节点内，上下键/回车才能冒泡到 cmdk 的键盘处理器。 */}
        <Command
          shouldFilter={false}
          loop
          value={active}
          onValueChange={setActive}
          className="flex flex-col"
        >
          <div className="flex items-center gap-2 border-b border-separator px-3">
            <SearchIcon className="size-3.5 shrink-0 text-icon-muted" />
            <input
              ref={inputRef}
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder={searchPlaceholder}
              aria-label={searchPlaceholder}
              className="h-9 w-full min-w-0 bg-transparent text-[13px] outline-none placeholder:text-icon-muted"
            />
          </div>

          <CommandList
            ref={listRef}
            style={{ maxHeight }}
            className="overflow-y-auto overscroll-contain p-1"
          >
            {filtered.length === 0 ? (
              <div className="px-3 py-7 text-center text-[12.5px] text-muted-foreground">
                {emptyText}
              </div>
            ) : (
              groups.map(([group, items]) => (
                <div key={group || '__flat__'}>
                  {group ? (
                    <p className="px-2.5 pt-2.5 pb-1 text-[11px] font-medium tracking-[0.06em] text-muted-foreground uppercase">
                      {group}
                    </p>
                  ) : null}
                  {items.map((o) => (
                    <CommandItem
                      key={o.value}
                      value={o.value}
                      disabled={o.disabled}
                      onSelect={() => pick(o.value)}
                      className={cn(
                        'relative flex h-8 cursor-default items-center gap-2 rounded-md py-1 pr-8 pl-2.5 text-[13px] outline-hidden select-none',
                        'data-[selected=true]:bg-selection data-[selected=true]:text-foreground',
                        'data-[disabled=true]:pointer-events-none data-[disabled=true]:opacity-50',
                        o.value === value && 'font-medium',
                      )}
                    >
                      <span className="min-w-0 flex-1 truncate">{o.label}</span>
                      {o.hint ? (
                        <span className="shrink-0 truncate text-[12px] text-muted-foreground">
                          {o.hint}
                        </span>
                      ) : null}
                      {o.value === value ? (
                        <CheckIcon
                          className="absolute right-2.5 size-3.5 text-brand"
                          strokeWidth={3}
                        />
                      ) : null}
                    </CommandItem>
                  ))}
                </div>
              ))
            )}
          </CommandList>
        </Command>
      </PopoverContent>
    </Popover>
  )
}
