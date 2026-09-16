import { useMemo } from 'react'

import { CopyButton } from './CopyButton'
import { cn } from '@/lib/utils'

export interface CodeBlockProps {
  code: string
  /** 语言。目前只对 json 做高亮。 */
  language?: 'json' | 'text' | 'bash'
  /** 右上角是否显示复制按钮。 */
  copyable?: boolean
  className?: string
  /** 最大化高度，超出滚动。 */
  maxHeight?: number
}

/**
 * 代码块。JSON 走一个极小的分词高亮（关键字 / 字符串 / 标点），
 * 不引入高亮库 —— 管理后台里需要高亮的只有 JSON 和命令行。
 */
export function CodeBlock({
  code,
  language = 'text',
  copyable = true,
  className,
  maxHeight,
}: CodeBlockProps) {
  const html = useMemo(
    () => (language === 'json' ? highlightJson(code) : escapeHtml(code)),
    [code, language],
  )

  return (
    <div className={cn('code-block group relative', className)}>
      {copyable ? (
        <div className="absolute top-2 right-2 opacity-0 transition-opacity group-hover:opacity-100 focus-within:opacity-100">
          <CopyButton value={code} label="复制代码" />
        </div>
      ) : null}
      <pre
        className="p-4 text-[12px] leading-[1.7]"
        style={maxHeight ? { maxHeight, overflow: 'auto' } : undefined}
      >
        <code dangerouslySetInnerHTML={{ __html: html }} />
      </pre>
    </div>
  )
}

function escapeHtml(s: string) {
  return s
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
}

/**
 * 极简 JSON 高亮。
 *
 * 先把每个 token 转义再包 span，避免在原文上做替换 ——
 * 在已插入标签的字符串上继续正则匹配，会把标签内容也吃掉。
 */
function highlightJson(raw: string): string {
  const out: string[] = []
  const re = /("(?:\\.|[^"\\])*")(\s*:)?|(\b-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?\b)|\b(true|false|null)\b|([{}[\],:])/g
  let last = 0
  let m: RegExpExecArray | null
  while ((m = re.exec(raw)) !== null) {
    if (m.index > last) out.push(escapeHtml(raw.slice(last, m.index)))
    if (m[1]) {
      // 后面跟着冒号的是 key
      out.push(`<span class="${m[2] ? 'json-key' : 'json-str'}">${escapeHtml(m[1])}</span>`)
      if (m[2]) out.push(`<span class="json-punct">${escapeHtml(m[2])}</span>`)
    } else if (m[3]) {
      out.push(`<span class="json-str">${escapeHtml(m[3])}</span>`)
    } else if (m[4]) {
      out.push(`<span class="json-key">${escapeHtml(m[4])}</span>`)
    } else if (m[5]) {
      out.push(`<span class="json-brace">${escapeHtml(m[5])}</span>`)
    }
    last = re.lastIndex
  }
  if (last < raw.length) out.push(escapeHtml(raw.slice(last)))
  return out.join('')
}
