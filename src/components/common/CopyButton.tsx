import { useCallback, useRef, useState } from 'react'

import { Icon } from './Icon'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { cn } from '@/lib/utils'

export interface CopyButtonProps {
  /** 要复制的文本。传函数可延迟取值（如从 DOM 读）。 */
  value: string | (() => string)
  label?: string
  size?: number
  className?: string
  /** 复制成功后的回调，用于 toast 或行内反馈。 */
  onCopied?: () => void
}

/** 复制按钮。优先 `navigator.clipboard`，失败回落到 `document.execCommand`——
 *  非 HTTPS 的局域网地址下 clipboard API 直接不可用。 */
export function CopyButton({
  value,
  label = '复制',
  size = 15,
  className,
  onCopied,
}: CopyButtonProps) {
  const [copied, setCopied] = useState(false)
  const timer = useRef<number | undefined>(undefined)

  const copy = useCallback(async () => {
    const text = typeof value === 'function' ? value() : value
    try {
      if (navigator.clipboard?.writeText) {
        await navigator.clipboard.writeText(text)
      } else if (!legacyCopy(text)) {
        throw new Error('execCommand 不可用')
      }
      setCopied(true)
      onCopied?.()
      window.clearTimeout(timer.current)
      timer.current = window.setTimeout(() => setCopied(false), 1400)
    } catch {
      setCopied(false)
    }
  }, [value, onCopied])

  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <button
          type="button"
          className={cn('copy-btn', copied && 'copied', className)}
          aria-label={copied ? '已复制' : label}
          onClick={() => void copy()}
        >
          <Icon name={copied ? 'check' : 'copy'} size={size} />
        </button>
      </TooltipTrigger>
      <TooltipContent>{copied ? '已复制' : label}</TooltipContent>
    </Tooltip>
  )
}

function legacyCopy(text: string) {
  const ta = document.createElement('textarea')
  ta.value = text
  ta.setAttribute('readonly', '')
  ta.style.position = 'fixed'
  ta.style.opacity = '0'
  document.body.appendChild(ta)
  ta.select()
  const ok = document.execCommand('copy')
  document.body.removeChild(ta)
  return ok
}
