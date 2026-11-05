import type { ReactNode } from 'react'

import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogBody,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Icon } from './Icon'
import { Spinner } from './States'

export interface ConfirmDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  title: string
  description: ReactNode
  confirmText?: string
  cancelText?: string
  /** 破坏性操作走红色按钮，并把标题的删除对象写清楚。 */
  destructive?: boolean
  pending?: boolean
  onConfirm: () => void
}

/** 确认对话框。与 FormDialog 共用同一套弹框皮肤（Header → Body → Footer，
 *  次要操作靠左、主操作靠右），破坏性操作走红色描边按钮 + 警示图标。 */
export function ConfirmDialog({
  open,
  onOpenChange,
  title,
  description,
  confirmText = '确认',
  cancelText = '取消',
  destructive,
  pending,
  onConfirm,
}: ConfirmDialogProps) {
  return (
    <Dialog open={open} onOpenChange={(next) => !pending && onOpenChange(next)}>
      <DialogContent size="sm">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <Icon
              name={destructive ? 'triangle-alert' : 'circle-info'}
              size={15}
              className={destructive ? 'text-warning-strong' : 'text-icon-muted'}
            />
            {title}
          </DialogTitle>
        </DialogHeader>

        <DialogBody>
          <DialogDescription asChild>
            <div className="text-[12.5px] leading-relaxed text-muted-foreground">{description}</div>
          </DialogDescription>
        </DialogBody>

        <DialogFooter>
          <Button variant="ghost" disabled={pending} onClick={() => onOpenChange(false)}>
            {cancelText}
          </Button>
          <Button variant={destructive ? 'destructive' : 'primary'} disabled={pending} onClick={onConfirm}>
            {pending ? <Spinner size={13} /> : null}
            {confirmText}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
