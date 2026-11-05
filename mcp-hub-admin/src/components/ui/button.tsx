import * as React from 'react'
import { cva, type VariantProps } from 'class-variance-authority'
import { Slot } from 'radix-ui'

import { cn } from '@/lib/utils'

/** 按钮。视觉全部走 apple-ui.css 的 `.ui-btn*`，这里只做变体映射与尺寸。
 *  刻意不用 Tailwind 的 h-9 / rounded-md：设计系统的控件高度是 30px，
 *  与 shadcn 默认的 36px 不是一套尺度，混用会让同排按钮高低不一。 */
const buttonVariants = cva('ui-btn', {
  variants: {
    variant: {
      default: 'ui-btn-primary',
      primary: 'ui-btn-primary',
      secondary: 'ui-btn-secondary',
      outline: 'ui-btn-secondary',
      ghost: 'ui-btn-ghost',
      quiet: 'ui-btn-ghost ui-btn-quiet',
      destructive: 'ui-btn-destructive',
      link: 'ui-btn-ghost h-auto! px-0! underline-offset-4 hover:underline',
    },
    size: {
      default: '',
      sm: 'h-[26px]! px-2.5! text-[12px]!',
      xs: 'h-[24px]! px-2! text-[11.5px]!',
      lg: 'h-[34px]! px-4! text-[13px]!',
      icon: 'icon-btn',
      'icon-sm': 'icon-btn size-[22px]!',
      'icon-lg': 'icon-btn size-[30px]!',
    },
  },
  defaultVariants: {
    variant: 'default',
    size: 'default',
  },
})

export interface ButtonProps
  extends React.ComponentProps<'button'>,
    VariantProps<typeof buttonVariants> {
  asChild?: boolean
}

function Button({
  className,
  variant,
  size,
  asChild = false,
  type,
  ...props
}: ButtonProps) {
  const Comp = asChild ? Slot.Root : 'button'
  return (
    <Comp
      data-slot="button"
      data-variant={variant ?? 'default'}
      data-size={size ?? 'default'}
      // 非 submit 的按钮默认 type="button"：表单内的按钮忘了写 type 会变成一次提交 + 整页刷新。
      type={asChild ? undefined : (type ?? 'button')}
      className={cn(buttonVariants({ variant, size }), className)}
      {...props}
    />
  )
}

export { Button, buttonVariants }
