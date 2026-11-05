import { ConfirmDialog } from '@/components/common/ConfirmDialog'
import { formatInt } from '@/lib/format'
import type { ServerSummary } from '@/lib/api/types'

interface Props {
  server: ServerSummary | null
  pending: boolean
  onOpenChange: (open: boolean) => void
  onConfirm: () => void
}

/** 删除 Server 的二次确认。文案必须带三个数：撤下多少工具、连带删掉多少密钥、
 *  不可撤销——删 server 会波及它的整个密钥池。 */
export function ServerDeleteDialog({ server, pending, onOpenChange, onConfirm }: Props) {
  return (
    <ConfirmDialog
      open={Boolean(server)}
      onOpenChange={onOpenChange}
      title={server ? `删除 Server「${server.name}」？` : '删除 Server？'}
      description={
        server ? (
          <div className="space-y-2">
            <p>
              将从聚合网关撤下 <code className="code-chip">{server.key}</code>，其暴露的{' '}
              <strong>{formatInt(server.exposedToolCount)}</strong> 个工具会立即对客户端不可见。
            </p>
            {server.keyCount > 0 ? (
              <p className="text-error-strong">
                连带删除该 Server 的 {formatInt(server.keyCount)} 把密钥，此操作不可撤销。
              </p>
            ) : null}
            <p className="text-muted-foreground">此操作不可撤销。如需暂时停止服务，请改用「停用」。</p>
          </div>
        ) : null
      }
      confirmText="删除"
      destructive
      pending={pending}
      onConfirm={onConfirm}
    />
  )
}
