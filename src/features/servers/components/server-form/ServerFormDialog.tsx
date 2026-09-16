import { FormDialog } from '@/components/common/FormDialog'
import { Icon } from '@/components/common/Icon'
import { Button } from '@/components/ui/button'

import { StepBar } from './StepBar'
import { Step1Transport } from './Step1Transport'
import { Step2Auth } from './Step2Auth'
import { Step3Probe } from './Step3Probe'
import { useServerWizard } from './use-server-wizard'

interface Props {
  open: boolean
  onOpenChange: (open: boolean) => void
}

/** 新增 Server 向导：先探活拿工具清单、再勾选要启用的工具（工具名来自探活，不可手抄）。
 *  本文件只做编排：状态在 use-server-wizard，三屏各自成文件。 */
export function ServerFormDialog({ open, onOpenChange }: Props) {
  const wizard = useServerWizard({ open, onOpenChange })
  const { step, form, errs, keyError, sigStale, categories, pending, set } = wizard

  return (
    <FormDialog
      open={open}
      onOpenChange={onOpenChange}
      size="xl"
      icon="plus"
      title="新增 Server"
      description={<span className="font-mono">POST /api/servers?probe=1</span>}
      pending={pending}
      /* 三步向导：左「取消」由模板给，右侧「上一步 / 下一步 / 创建」自己排。 */
      footerExtra={
        step > 1 ? (
          <Button variant="outline" onClick={wizard.goBack} disabled={pending}>
            上一步
          </Button>
        ) : null
      }
      footerEnd={
        step < 3 ? (
          <Button variant="primary" onClick={wizard.goNext}>
            下一步
          </Button>
        ) : (
          <Button variant="primary" onClick={wizard.submit} disabled={pending}>
            {pending ? (
              <Icon name="refresh-cw" size={14} className="animate-spin" />
            ) : (
              <Icon name="check" size={14} />
            )}
            创建
          </Button>
        )
      }
    >
      <StepBar step={step} />

      {step === 1 ? (
        <Step1Transport f={form} set={set} errs={errs} keyError={keyError} />
      ) : step === 2 ? (
        <Step2Auth f={form} set={set} errs={errs} categories={categories} />
      ) : (
        <Step3Probe f={form} set={set} onProbe={wizard.runProbe} sigStale={sigStale} />
      )}
    </FormDialog>
  )
}
