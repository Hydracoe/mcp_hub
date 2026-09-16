import * as React from "react"
import { cva, type VariantProps } from "class-variance-authority"
import { cn } from "@/lib/utils"

/* 图标列展开有两种触发方式：原生 <svg>（shadcn 默认）与项目的
   <i data-icon>（Icon 组件，CSS mask）。后者不命中 has-[>svg] 时
   图标列宽仍是 0，图标会叠到标题上 —— 这就是"重叠"的根因。 */
const alertVariants = cva(
  "relative grid w-full grid-cols-[0_1fr] items-start gap-y-0.5 rounded-xl border px-4 py-3 text-[13px] has-[>svg]:grid-cols-[calc(var(--spacing)*4)_1fr] has-[>svg]:gap-x-3 has-[>[data-icon]]:grid-cols-[calc(var(--spacing)*4)_1fr] has-[>[data-icon]]:gap-x-3 [&>svg]:size-4 [&>svg]:translate-y-0.5 [&>svg]:text-current [&>[data-icon]]:translate-y-0.5 [&>[data-icon]]:text-current",
  {
    variants: {
      variant: {
        default: "bg-card text-foreground border-border",
        info: "bg-brand/10 text-foreground border-brand/25",
        success: "bg-success-surface text-foreground border-success/25",
        warning: "bg-warning-surface text-foreground border-warning/25",
        destructive: "bg-error-surface text-foreground border-error/25",
      },
    },
    defaultVariants: {
      variant: "default",
    },
  }
)

function Alert({
  className,
  variant,
  ...props
}: React.ComponentProps<"div"> & VariantProps<typeof alertVariants>) {
  return (
    <div
      data-slot="alert"
      role="alert"
      className={cn(alertVariants({ variant }), className)}
      {...props}
    />
  )
}

function AlertTitle({ className, ...props }: React.ComponentProps<"div">) {
  return (
    <div
      data-slot="alert-title"
      className={cn(
        "col-start-2 line-clamp-1 min-h-4 text-[13px] font-semibold tracking-tight",
        className
      )}
      {...props}
    />
  )
}

function AlertDescription({
  className,
  ...props
}: React.ComponentProps<"div">) {
  return (
    <div
      data-slot="alert-description"
      className={cn(
        "col-start-2 grid justify-items-start gap-1 text-[12.5px] text-muted-foreground [&_p]:leading-relaxed",
        className
      )}
      {...props}
    />
  )
}

export { Alert, AlertTitle, AlertDescription }
