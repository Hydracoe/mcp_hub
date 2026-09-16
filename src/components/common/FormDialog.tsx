import type { ReactNode } from 'react'

import { Icon, type IconName } from './Icon'
import { Spinner } from './States'
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

export interface FormDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  title: ReactNode
  /** 标题左侧图标。头部带不带图标之前是一半对一半，模板里统一成「给了就渲染」。 */
  icon?: IconName
  description?: ReactNode
  size?: 'sm' | 'default' | 'lg' | 'xl'
  /** 提交中：禁用页脚全部按钮并替换成转圈。 */
  pending?: boolean
  submitText?: string
  cancelText?: string
  /**
   * 不渲染「取消」。用于一次性的交付 / 结果页：那时候关掉弹框
   * 不是一个"放弃操作"的选项，留一个取消按钮只会让人以为
   * 这一步还能反悔。
   */
  hideCancel?: boolean
  /** 不传就没有页脚（只读查看类弹框）。 */
  onSubmit?: () => void
  /** 提交按钮左侧的次级操作，例如向导弹框的「上一步」。 */
  footerExtra?: ReactNode
  /** 提交按钮位整块替换，向导弹框最后一屏以外的场景用它。 */
  footerEnd?: ReactNode
  /** 表单级错误文案，渲染在正文末尾。 */
  error?: ReactNode
  bodyClassName?: string
  children: ReactNode
}

/**
 * 表单弹框模板。
 *
 * 页面只给「标题 + 正文 + 提交动作」，结构固定为
 * Header（图标 / 标题 / 副标题）→ Body（内边距与间距由模板定）
 * → Footer（次要操作靠左、主操作靠右）。
 *
 * 收这个模板是为了终结三种分叉：正文手写 6 遍不同间距、
 * 页脚一半靠左一半靠右、标题一半带图标一半不带。
 *
 * 只读弹框（没有提交动作、没有页脚）不要用在这里 ——
 * 直接用 `DialogContent` + `DialogBody`。
 */
export function FormDialog({
  open,
  onOpenChange,
  title,
  icon,
  description,
  size = 'lg',
  pending,
  submitText = '保存',
  cancelText = '取消',
  hideCancel,
  onSubmit,
  footerExtra,
  footerEnd,
  error,
  bodyClassName,
  children,
}: FormDialogProps) {
  const hasFooter = Boolean(onSubmit || footerExtra || footerEnd)

  return (
    <Dialog open={open} onOpenChange={(o) => !pending && onOpenChange(o)}>
      <DialogContent size={size}>
        <DialogHeader>
          <DialogTitle className={icon ? 'flex items-center gap-2' : undefined}>
            {icon ? <Icon name={icon} size={16} className="text-icon-muted" /> : null}
            {title}
          </DialogTitle>
          {description ? <DialogDescription>{description}</DialogDescription> : null}
        </DialogHeader>

        <DialogBody className={bodyClassName}>
          {children}
          {error ? (
            <p className="flex items-center gap-1.5 text-[12.5px] text-error-strong">
              <Icon name="circle-alert" size={14} />
              {error}
            </p>
          ) : null}
        </DialogBody>

        {hasFooter ? (
          <DialogFooter>
            {/* 只有取消时才撑左侧：取消没了，主操作仍应靠右 */}
            {hideCancel ? (
              <span />
            ) : (
              <Button variant="ghost" onClick={() => onOpenChange(false)} disabled={pending}>
                {cancelText}
              </Button>
            )}
            <div className="flex items-center gap-2">
              {footerExtra}
              {footerEnd ??
                (onSubmit ? (
                  <Button variant="primary" onClick={onSubmit} disabled={pending}>
                    {pending ? <Spinner size={13} /> : null}
                    {submitText}
                  </Button>
                ) : null)}
            </div>
          </DialogFooter>
        ) : null}
      </DialogContent>
    </Dialog>
  )
}
