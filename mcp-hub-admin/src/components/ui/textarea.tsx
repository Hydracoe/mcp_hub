import * as React from 'react'

import { cn } from '@/lib/utils'

/** 多行输入。等宽字体、可纵向拉伸，适合 JSON / env / headers 行。 */
function Textarea({ className, ...props }: React.ComponentProps<'textarea'>) {
  return (
    <textarea
      data-slot="textarea"
      className={cn('form-textarea', className)}
      {...props}
    />
  )
}

export { Textarea }
