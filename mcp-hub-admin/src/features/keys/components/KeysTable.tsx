import { useState } from 'react'

import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Switch } from '@/components/ui/switch'
import { TableCell, TableHead, TableRow } from '@/components/ui/table'
import { TableFrame } from '@/components/common/TableFrame'
import type { TableFrameProps } from '@/components/common/TableFrame'
import { ConfirmDialog } from '@/components/common/ConfirmDialog'
import { Icon } from '@/components/common/Icon'
import { formatDateTime, formatRelative } from '@/lib/format'
import { toastError, toastSuccess } from '@/lib/toast'
import type { ApiKeyView } from '@/lib/api/types'
import type { useApiKeyMutations } from '@/lib/api/queries'

type Mutations = ReturnType<typeof useApiKeyMutations>

interface KeysTableProps {
  items: ApiKeyView[]
  mutations: Mutations
  frame?: Omit<TableFrameProps, 'head' | 'colCount' | 'children'>
}

const COLS = ['名称', '前缀', '状态', '最近使用', '创建时间', '操作']

export function KeysTable({ items, mutations, frame }: KeysTableProps) {
  const [deleteTarget, setDeleteTarget] = useState<ApiKeyView | null>(null)

  const confirmDelete = async () => {
    if (!deleteTarget) return
    try {
      await mutations.remove.mutateAsync(deleteTarget.id)
      toastSuccess('已删除密钥', '此后用它的请求会立即收到 401')
    } catch (e) {
      toastError(e)
    } finally {
      setDeleteTarget(null)
    }
  }

  const toggle = async (k: ApiKeyView, enabled: boolean) => {
    try {
      await mutations.setEnabled.mutateAsync({ id: k.id, enabled })
      // 停用是收紧不是放开：库里还存在 key，端点的鉴权要求不会因此消失。
      toastSuccess(enabled ? '已启用密钥' : '已停用密钥', enabled ? undefined : '端点仍在鉴权状态')
    } catch (e) {
      toastError(e)
    }
  }

  return (
    <>
      <TableFrame
        {...frame}
        colCount={COLS.length}
        tableClassName="min-w-[880px]"
        head={
          <TableRow>
            {COLS.map((h) => (
              <TableHead key={h}>{h}</TableHead>
            ))}
          </TableRow>
        }
      >
        {items.map((k) => (
          <TableRow key={k.id}>
            <TableCell>
              <div className="font-medium text-foreground">{k.name}</div>
              <div className="cell-sub font-mono text-muted-foreground">{k.id}</div>
            </TableCell>
            <TableCell>
              <span className="font-mono text-muted-foreground">{k.prefix}…</span>
            </TableCell>
            <TableCell>
              <div className="flex items-center gap-2">
                <Badge variant={k.enabled ? 'success' : 'neutral'}>
                  {k.enabled ? '已启用' : '已停用'}
                </Badge>
                <Switch
                  checked={k.enabled}
                  disabled={mutations.setEnabled.isPending}
                  onCheckedChange={(v) => void toggle(k, v)}
                  aria-label="启用 / 停用"
                />
              </div>
            </TableCell>
            <TableCell>
              {/* 0 表示从未调用过 —— 不能当 1970 年渲染成「20000d ago」 */}
              {k.lastUsedAt ? (
                <span className="text-muted-foreground">{formatRelative(k.lastUsedAt)}</span>
              ) : (
                <span className="text-muted-foreground">从未使用</span>
              )}
            </TableCell>
            <TableCell>
              <span className="font-mono tabular-nums text-muted-foreground">
                {formatDateTime(k.createdAt)}
              </span>
            </TableCell>
            <TableCell>
              <Button
                size="icon-sm"
                variant="ghost"
                aria-label="删除"
                onClick={() => setDeleteTarget(k)}
              >
                <Icon name="trash-2" size={13} />
              </Button>
            </TableCell>
          </TableRow>
        ))}
      </TableFrame>

      <ConfirmDialog
        open={Boolean(deleteTarget)}
        onOpenChange={(o) => !o && setDeleteTarget(null)}
        destructive
        title={`删除密钥${deleteTarget ? `「${deleteTarget.name}」` : ''}`}
        description={
          <>
            明文无法找回，删除后客户端会立刻收到 401，只能重新创建一把并更新配置。
            如果只是临时收回权限，用「停用」更合适 —— 记录留着，可随时恢复。
          </>
        }
        confirmText="确认删除"
        pending={mutations.remove.isPending}
        onConfirm={confirmDelete}
      />
    </>
  )
}
