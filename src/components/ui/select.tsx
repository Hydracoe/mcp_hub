"use client"

import * as React from "react"
import { cn } from "@/lib/utils"
import { CheckIcon, ChevronDownIcon, ChevronUpIcon } from "lucide-react"
import { Select as SelectPrimitive } from "radix-ui"

/**
 * 下拉选择（Radix Select）。
 *
 * 适用：**选项 ≤ 8 个**的枚举 —— 传输类型、鉴权方式、过滤模式、每页条数。
 * 选项更多（错误类型 19 项、Server 列表、下游 enum 参数）请用
 * `@/components/common/Combobox`：那个带搜索框，键盘导航也更好。
 *
 * 外观收口在三处，调用点只声明宽度：
 *   .ui-select-trigger  触发器（CSS，锁高度 32px / 字号 13px）
 *   .ui-popover         浮层皮（CSS，与 Dialog/Sheet 同一套令牌）
 *   SelectItem          行高、勾选位在下面定义
 */
function Select({
  ...props
}: React.ComponentProps<typeof SelectPrimitive.Root>) {
  return <SelectPrimitive.Root data-slot="select" {...props} />
}

function SelectGroup({
  ...props
}: React.ComponentProps<typeof SelectPrimitive.Group>) {
  return <SelectPrimitive.Group data-slot="select-group" {...props} />
}

function SelectValue({
  ...props
}: React.ComponentProps<typeof SelectPrimitive.Value>) {
  return <SelectPrimitive.Value data-slot="select-value" {...props} />
}

function SelectTrigger({
  className,
  size = "default",
  children,
  ...props
}: React.ComponentProps<typeof SelectPrimitive.Trigger> & {
  size?: "sm" | "default"
}) {
  return (
    <SelectPrimitive.Trigger
      data-slot="select-trigger"
      data-size={size}
      className={cn(
        "ui-select-trigger group inline-flex items-center justify-between gap-2",
        /* 值过长时压住省略号；min-w-0 是 flex 子项能收缩的前提 */
        "*:data-[slot=select-value]:flex *:data-[slot=select-value]:min-w-0 *:data-[slot=select-value]:items-center *:data-[slot=select-value]:gap-2 *:data-[slot=select-value]:truncate",
        className
      )}
      {...props}
    >
      {children}
      {/* 箭头由组件渲染，不再走 CSS background-image —— 那个的颜色写死了，
          深色模式不跟随。打开时旋转 180° 给一点反馈。 */}
      <SelectPrimitive.Icon asChild>
        <ChevronDownIcon className="size-4 shrink-0 text-icon-muted transition-transform duration-200 group-data-[state=open]:rotate-180" />
      </SelectPrimitive.Icon>
    </SelectPrimitive.Trigger>
  )
}

function SelectContent({
  className,
  children,
  position = "popper",
  align = "start",
  ...props
}: React.ComponentProps<typeof SelectPrimitive.Content>) {
  return (
    <SelectPrimitive.Portal>
      <SelectPrimitive.Content
        data-slot="select-content"
        className={cn(
          "ui-popover relative z-50 max-h-(--radix-select-content-available-height) min-w-32 origin-(--radix-select-content-transform-origin) overflow-x-hidden overflow-y-auto",
          "data-[side=bottom]:slide-in-from-top-2 data-[side=left]:slide-in-from-right-2 data-[side=right]:slide-in-from-left-2 data-[side=top]:slide-in-from-bottom-2 data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=closed]:zoom-out-95 data-[state=open]:animate-in data-[state=open]:fade-in-0 data-[state=open]:zoom-in-95",
          position === "popper" &&
          "data-[side=bottom]:translate-y-1 data-[side=left]:-translate-x-1 data-[side=right]:translate-x-1 data-[side=top]:-translate-y-1",
          className
        )}
        /* 默认 popper：菜单贴着触发器展开。
           shadcn 默认的 item-aligned 会把菜单**对齐到当前选中项**，
           19 项里选中最后一项时菜单会往上翻一大截，筛选场景很跳。 */
        position={position}
        align={align}
        {...props}
      >
        <SelectScrollUpButton />
        <SelectPrimitive.Viewport
          className={cn(
            /* 内边距只在这里给一次。此前 Content 和 Viewport 各写一个 p-1，
               叠加成 8px 内缩。 */
            "p-1",
            position === "popper" &&
            "h-(--radix-select-trigger-height) w-full min-w-(--radix-select-trigger-width) scroll-my-1"
          )}
        >
          {children}
        </SelectPrimitive.Viewport>
        <SelectScrollDownButton />
      </SelectPrimitive.Content>
    </SelectPrimitive.Portal>
  )
}

function SelectLabel({
  className,
  ...props
}: React.ComponentProps<typeof SelectPrimitive.Label>) {
  return (
    <SelectPrimitive.Label
      data-slot="select-label"
      className={cn("px-2.5 py-1.5 text-[11px] font-medium tracking-[0.06em] text-muted-foreground uppercase", className)}
      {...props}
    />
  )
}

function SelectItem({
  className,
  children,
  ...props
}: React.ComponentProps<typeof SelectPrimitive.Item>) {
  return (
    <SelectPrimitive.Item
      data-slot="select-item"
      className={cn(
        "relative flex h-8 w-full cursor-default items-center gap-2 rounded-md py-1 pr-8 pl-2.5 text-[13px] outline-hidden select-none",
        /* data-highlighted 是 Radix 给的键盘/指针高亮位；
           data-state=checked 是当前选中项。两者此前都引用 bg-selection，
           而 --color-selection 没注册，实际一点底色都没有。 */
        "data-[highlighted]:bg-selection data-[highlighted]:text-foreground",
        "data-[state=checked]:bg-selection data-[state=checked]:font-medium data-[state=checked]:text-foreground",
        "data-disabled:pointer-events-none data-disabled:opacity-50",
        "[&_svg]:pointer-events-none [&_svg]:shrink-0 [&_svg:not([class*='size-'])]:size-4",
        className
      )}
      {...props}
    >
      <SelectPrimitive.ItemText>{children}</SelectPrimitive.ItemText>
      <span
        data-slot="select-item-indicator"
        className="absolute right-2.5 flex size-3.5 items-center justify-center text-brand"
      >
        <SelectPrimitive.ItemIndicator>
          <CheckIcon className="size-3.5" strokeWidth={3} />
        </SelectPrimitive.ItemIndicator>
      </span>
    </SelectPrimitive.Item>
  )
}

function SelectSeparator({
  className,
  ...props
}: React.ComponentProps<typeof SelectPrimitive.Separator>) {
  return (
    <SelectPrimitive.Separator
      data-slot="select-separator"
      className={cn("pointer-events-none -mx-1 my-1 h-px bg-separator", className)}
      {...props}
    />
  )
}

/* 滚动提示条：给底色，否则菜单文字会从箭头底下透出来。 */
function SelectScrollUpButton({
  className,
  ...props
}: React.ComponentProps<typeof SelectPrimitive.ScrollUpButton>) {
  return (
    <SelectPrimitive.ScrollUpButton
      data-slot="select-scroll-up-button"
      className={cn(
        "z-10 -mt-1 flex h-6 cursor-default items-center justify-center bg-popover text-icon-muted",
        className
      )}
      {...props}
    >
      <ChevronUpIcon className="size-3.5" />
    </SelectPrimitive.ScrollUpButton>
  )
}

function SelectScrollDownButton({
  className,
  ...props
}: React.ComponentProps<typeof SelectPrimitive.ScrollDownButton>) {
  return (
    <SelectPrimitive.ScrollDownButton
      data-slot="select-scroll-down-button"
      className={cn(
        "z-10 -mb-1 flex h-6 cursor-default items-center justify-center bg-popover text-icon-muted",
        className
      )}
      {...props}
    >
      <ChevronDownIcon className="size-3.5" />
    </SelectPrimitive.ScrollDownButton>
  )
}

export {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectLabel,
  SelectScrollDownButton,
  SelectScrollUpButton,
  SelectSeparator,
  SelectTrigger,
  SelectValue,
}
