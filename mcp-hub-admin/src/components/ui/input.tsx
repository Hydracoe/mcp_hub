import * as React from 'react'

import { cn } from '@/lib/utils'

/** 单行输入。样式来自设计系统的 `.form-input`（32px 高、13px 字号）。 */
function Input({ className, type, ...props }: React.ComponentProps<'input'>) {
  return (
    <input
      type={type}
      data-slot="input"
      className={cn('form-input', className)}
      {...props}
    />
  )
}

export { Input }
