import { ICON_NAME_SET, type IconName } from './icon-names'
import { cn } from '@/lib/utils'

export type { IconName }

export interface IconProps extends React.HTMLAttributes<HTMLElement> {
  /** 图标名。取值来自 assets/icons/apple/*.svg（设计稿自带资源）。 */
  name: IconName
  /**
   * 尺寸。默认 16px，与设计稿中 `w-4 h-4` 的绝大多数用法一致。
   * 尺寸必须由调用处给出 —— icons.css 刻意不写 width/height，
   * 否则它的属性选择器会静默压掉 Tailwind 的 size-*。
   */
  size?: number
  /** 无障碍标签。默认装饰性图标（aria-hidden），仅在有语义时传入。 */
  label?: string
}

/**
 * 基于 CSS mask 的本地图标。
 *
 * 用 `mask-image` 而不是 `<img>`：图标会继承 `currentColor`，
 * hover / 禁用 / 深色模式无需各准备一份资源。
 */
export function Icon({ name, size = 16, label, className, style, ...props }: IconProps) {
  if (import.meta.env.DEV && !ICON_NAME_SET.has(name)) {
    // 写错名字时图标会整块消失且没有任何报错，是最难自查的一类问题。
    console.warn(`[Icon] 未注册的图标名: ${name}`)
  }
  return (
    <i
      data-icon={name}
      className={cn('inline-flex shrink-0', className)}
      style={{ width: size, height: size, ...style }}
      aria-hidden={label ? undefined : true}
      aria-label={label}
      role={label ? 'img' : undefined}
      {...props}
    />
  )
}
