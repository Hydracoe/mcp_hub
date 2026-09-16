import * as React from "react"
import { cva, type VariantProps } from "class-variance-authority"
import { cn } from "@/lib/utils"
import { Label as LabelPrimitive } from "radix-ui"

const labelVariants = cva("", {
  variants: {
    variant: {
      default:
        "text-[11px] font-semibold uppercase tracking-[0.06em] text-muted-foreground",
      plain:
        "text-[12.5px] font-medium normal-case tracking-normal text-muted-foreground",
    },
  },
  defaultVariants: {
    variant: "default",
  },
})

function Label({
  className,
  variant,
  ...props
}: React.ComponentProps<typeof LabelPrimitive.Root> &
  VariantProps<typeof labelVariants>) {
  return (
    <LabelPrimitive.Root
      data-slot="label"
      className={cn(
        "flex items-center gap-2 leading-none select-none group-data-[disabled=true]:pointer-events-none group-data-[disabled=true]:opacity-50 peer-disabled:cursor-not-allowed peer-disabled:opacity-50",
        labelVariants({ variant }),
        className
      )}
      {...props}
    />
  )
}

export { Label }
