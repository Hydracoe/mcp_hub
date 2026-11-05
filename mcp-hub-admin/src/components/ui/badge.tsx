import * as React from 'react'
import { cva, type VariantProps } from 'class-variance-authority'
import { Slot } from 'radix-ui'

import { cn } from '@/lib/utils'

/** 徽标。语义色只允许 success/warning/error/neutral/brand，不接受内联色值——否则同一状态会出现多种透明度。 */
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
