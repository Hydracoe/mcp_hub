import { ConfirmDialog } from '@/components/common/ConfirmDialog'

interface Props {
  open: boolean
  onDismiss: () => void
  onConfirm: () => void
}

/** 离开页面前的未保存提示。「离开」走 blocker.proceed，「留下」走 blocker.reset；
 *  关闭弹框等同于「留下」。 */
export function UnsavedChangesDialog({ open, onDismiss, onConfirm }: Props) {
  return (
    <ConfirmDialog
      open={open}
      onOpenChange={(o) => {
        if (!o) onDismiss()
      }}
      title="有未保存的修改"
      description="离开此页将丢弃本次修改，确定继续吗？"
      confirmText="丢弃并离开"
      cancelText="留下"
      destructive
      onConfirm={onConfirm}
    />
  )
}
