import * as React from 'react'
import { cva, type VariantProps } from 'class-variance-authority'
import { Slot } from 'radix-ui'

import { cn } from '@/lib/utils'

/**
 * 徽标。语义色只允许从 success / warning / error / neutral / brand 里选，
 * 不接受内联色值 —— 设计稿里同一个「healthy」徽标在 5 个页面里用了
 * 4 种不同的 color-mix 透明度，这就是它们的来源。
 */
const badgeVariants = cva('badge', {
  variants: {
    variant: {
      default: 'badge-neutral',
      neutral: 'badge-neutral',
      success: 'badge-success',
      warning: 'badge-warning',
      error: 'badge-error',
      destructive: 'badge-error',
      brand: 'bg-brand/12 text-brand-strong',
      outline: 'border border-border text-muted-foreground',
    },
    size: {
      default: '',
      lg: 'h-[22px]! px-2! text-[11.5px]!',
    },
  },
  defaultVariants: { variant: 'default', size: 'default' },
})

export interface BadgeProps
  extends React.ComponentProps<'span'>,
    VariantProps<typeof badgeVariants> {
  asChild?: boolean
}

function Badge({ className, variant, size, asChild = false, ...props }: BadgeProps) {
  const Comp = asChild ? Slot.Root : 'span'
  return (
    <Comp
      data-slot="badge"
      data-variant={variant ?? 'default'}
      className={cn(badgeVariants({ variant, size }), className)}
      {...props}
    />
  )
}

export { Badge, badgeVariants }
