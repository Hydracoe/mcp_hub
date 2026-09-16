import { Toaster as Sonner, type ToasterProps } from 'sonner'

import { Icon } from '@/components/common/Icon'

/** 全局提示（toast）。图标用项目自带 SVG 保持全站一套图标。 */
const Toaster = ({ ...props }: ToasterProps) => (
  <Sonner
    className="toaster group"
    position="bottom-right"
    icons={{
      success: <Icon name="check-circle" size={15} />,
      info: <Icon name="circle-info" size={15} />,
      warning: <Icon name="triangle-alert" size={15} />,
      error: <Icon name="x-circle" size={15} />,
      loading: <Icon name="refresh-cw" size={15} className="animate-spin" />,
    }}
    toastOptions={{
      classNames: {
        toast:
          'group toast !bg-popover !text-popover-foreground !border-border !rounded-xl !shadow-lg !text-[12.5px]',
        description: '!text-muted-foreground',
        actionButton: '!bg-brand !text-white !rounded-md !text-[11.5px]',
        cancelButton: '!bg-fill-tertiary !text-foreground !rounded-md !text-[11.5px]',
      },
    }}
    {...props}
  />
)

export { Toaster }
