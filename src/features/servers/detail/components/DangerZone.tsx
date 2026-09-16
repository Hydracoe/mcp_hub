import { useState } from 'react'

import { Icon } from '@/components/common/Icon'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Separator as UiSeparator } from '@/components/ui/separator'
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { formatInt } from '@/lib/format'
import type { ServerDetail } from '@/lib/api/types'

export interface DangerZoneProps {
  server: ServerDetail
  onDelete: () => void
  deleting: boolean
  onToggleEnabled: () => void
  enabling: boolean
}

export function DangerZone({ server, onDelete, deleting, onToggleEnabled, enabling }: DangerZoneProps) {
  const [open, setOpen] = useState(false)
  const [typed, setTyped] = useState('')
  const canConfirm = typed === server.key

  return (
    <div className="form-card">
      <div className="form-section-header">危险区</div>
      <div className="form-section-body space-y-3">
        <div className="flex items-center justify-between gap-3">
          <div>
            <p className="text-[13px] font-medium text-foreground">启用状态</p>
            <p className="form-hint mt-0.5">
              停用后从聚合列表移除，工具对 AI 客户端立即不可见
            </p>
          </div>
          <Button variant={server.enabled ? 'secondary' : 'outline'} size="sm" onClick={onToggleEnabled} disabled={enabling}>
            {enabling ? <Icon name="refresh-cw" size={13} className="animate-spin" /> : null}
            {server.enabled ? '停用' : '启用'}
          </Button>
        </div>

        <UiSeparator />

        <div className="flex items-center justify-between gap-3">
          <div>
            <p className="text-[13px] font-medium text-error-strong">删除 Server</p>
            <p className="form-hint mt-0.5">
              将移除全部 {formatInt(server.toolCount)} 个工具，并连带删除其密钥池
            </p>
          </div>
          <Button variant="destructive" size="sm" onClick={() => setOpen(true)}>
            <Icon name="trash-2" size={14} />
            删除
          </Button>
        </div>
      </div>

      <AlertDialog
        open={open}
        onOpenChange={(next) => {
          setOpen(next)
          if (!next) setTyped('')
        }}
      >
        <AlertDialogContent size="sm">
          <AlertDialogHeader>
            <AlertDialogTitle>删除 Server「{server.key}」？</AlertDialogTitle>
            <AlertDialogDescription asChild>
              <div className="text-[12.5px] leading-relaxed text-muted-foreground">
                将从聚合列表移除全部 {formatInt(server.toolCount)} 个工具，并连带删除其密钥池。此操作不可撤销。
                正在执行中的调用将被中断并标记为 CANCELED。
                <div className="mt-3">
                  <label className="form-label mb-1">输入 {server.key} 以确认</label>
                  <Input
                    value={typed}
                    onChange={(e) => setTyped(e.target.value)}
                    placeholder={server.key}
                    className="font-mono"
                    autoFocus
                  />
                </div>
              </div>
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={deleting}>取消</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              disabled={!canConfirm || deleting}
              onClick={(e) => {
                e.preventDefault()
                onDelete()
                setOpen(false)
              }}
            >
              {deleting ? <Icon name="refresh-cw" size={13} className="animate-spin" /> : null}
              确认删除
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}
