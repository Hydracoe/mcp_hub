import { Switch as SwitchPrimitive } from 'radix-ui'

import { cn } from '@/lib/utils'

/** 开关。用 Radix 保证 role="switch"/Space 键/aria-checked；视觉走设计系统的 `.switch`+`.knob`（38×22，绿色选中态）。 */
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
