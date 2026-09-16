import { useEffect, useRef, useState } from 'react'

import { Icon } from './Icon'
import { cn } from '@/lib/utils'

export interface SearchInputProps {
  value: string
  onChange: (value: string) => void
  placeholder?: string
  /** 防抖毫秒。输入即筛选的场景需要，按回车提交的场景传 0。 */
  debounceMs?: number
  className?: string
  'aria-label'?: string
}

/**
 * 带放大镜的搜索框。
 *
 * 内部维护一份受控副本 + 防抖，避免每敲一个字符都打一次接口；
 * 外部值变化（如清空筛选）时会同步回来。
 */
export function SearchInput({
  value,
  onChange,
  placeholder = '搜索…',
  debounceMs = 220,
  className,
  'aria-label': ariaLabel,
}: SearchInputProps) {
  const [draft, setDraft] = useState(value)
  const timer = useRef<number | undefined>(undefined)
  const onChangeRef = useRef(onChange)
  onChangeRef.current = onChange

  useEffect(() => {
    setDraft(value)
  }, [value])

  useEffect(() => () => window.clearTimeout(timer.current), [])

  return (
    <div className={cn('relative', className)}>
      <Icon
        name="search"
        size={16}
        className="pointer-events-none absolute top-1/2 left-3 -translate-y-1/2 text-muted-foreground"
      />
      <input
        type="search"
        aria-label={ariaLabel ?? placeholder}
        value={draft}
        placeholder={placeholder}
        onChange={(e) => {
          const next = e.target.value
          setDraft(next)
          window.clearTimeout(timer.current)
          if (debounceMs <= 0) {
            onChangeRef.current(next.trim())
            return
          }
          timer.current = window.setTimeout(() => onChangeRef.current(next.trim()), debounceMs)
        }}
        className="filter-input pl-9 pr-3 text-[13px]"
      />
    </div>
  )
}
