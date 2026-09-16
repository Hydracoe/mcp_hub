import { Switch as SwitchPrimitive } from 'radix-ui'

import { cn } from '@/lib/utils'

/**
 * 开关。用 Radix 保证 role="switch" / Space 键 / aria-checked，
 * 视觉用设计系统的 `.switch` + `.knob`（38×22，绿色选中态）。
 *
 * 设计稿原版是 `<label class="switch"><input type="checkbox">`，
 * 状态靠 `body:has(#singleKeyToggle:checked)` 从 DOM 里反查 —— 一页只能有一个。
 * 列表页每行都有开关，那条路走不通。
 */
function Switch({
  className,
  ...props
}: React.ComponentProps<typeof SwitchPrimitive.Root>) {
  return (
    <SwitchPrimitive.Root
      data-slot="switch"
      className={cn('switch', className)}
      {...props}
    >
      <SwitchPrimitive.Thumb data-slot="switch-thumb" className="knob" />
    </SwitchPrimitive.Root>
  )
}

export { Switch }
