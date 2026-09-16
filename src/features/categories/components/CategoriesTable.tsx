import { useState } from 'react'

import { Button } from '@/components/ui/button'
import { TableCell, TableHead, TableRow } from '@/components/ui/table'
import { TableFrame } from '@/components/common/TableFrame'
import type { TableFrameProps } from '@/components/common/TableFrame'
import { Icon } from '@/components/common/Icon'
import { ConfirmDialog } from '@/components/common/ConfirmDialog'
import { Dash } from '@/components/common/KeyValue'
import { formatInt } from '@/lib/format'
import { toastError, toastSuccess } from '@/lib/toast'
import type { Category } from '@/lib/api/types'
import type { useCategoryMutations } from '@/lib/api/queries'

type Mutations = ReturnType<typeof useCategoryMutations>

interface CategoriesTableProps {
  items: Category[]
  /** 外壳、状态位与页脚统一交给 TableFrame。 */
  frame?: Omit<TableFrameProps, 'head' | 'colCount' | 'children'>
  mutations: Mutations
  onEdit: (c: Category) => void
}

const COLS = ['中文名', '英文名', '描述', 'Server', '工具', '操作']

/**
 * 分类列表。
 *
 * 一列「英文名」承担了两件事：它既是展示字段，也是对外工具名的
 * 第一段。存量分类可能没有英文名（历史数据按中文名折不出来），
 * 这类行就地标出来 —— 否则用户看到工具名前缀是随机 id，
 * 只会以为是后端在乱起名。
 */
export function CategoriesTable({ items, frame, mutations, onEdit }: CategoriesTableProps) {
  const [deleteTarget, setDeleteTarget] = useState<Category | null>(null)

  const confirmDelete = async () => {
    if (!deleteTarget) return
    try {
      await mutations.remove.mutateAsync(deleteTarget.id)
      toastSuccess('已删除分类', '原属该分类的 Server 已转为未分类')
    } catch (e) {
      toastError(e)
    } finally {
      setDeleteTarget(null)
    }
  }

  return (
    <>
      <TableFrame
        {...frame}
        colCount={COLS.length}
        tableClassName="min-w-[860px]"
        head={
          <TableRow>
            {COLS.map((h) => (
              <TableHead key={h}>{h}</TableHead>
            ))}
          </TableRow>
        }
      >
        {items.map((c) => (
          <TableRow key={c.id}>
            <TableCell className="font-medium">{c.name}</TableCell>
            <TableCell>
              {c.slug ? (
                <code className="code-chip">{c.slug}</code>
              ) : (
                <span
                  className="inline-flex items-center gap-1 cell-sub text-warning-strong"
                  title="按中文名折不出英文名，工具名前缀暂时回退成分类 id"
                >
                  <Icon name="alert-triangle" size={12} />
                  未设英文名
                </span>
              )}
            </TableCell>
            <TableCell className="max-w-[320px] text-muted-foreground">
              {c.description ? (
                <span className="line-clamp-2">{c.description}</span>
              ) : (
                <Dash />
              )}
            </TableCell>
            <TableCell className="tabular-nums">{formatInt(c.serverCount ?? 0)}</TableCell>
            <TableCell className="tabular-nums">{formatInt(c.toolCount ?? 0)}</TableCell>
            <TableCell>
              <div className="flex items-center gap-1">
                <Button variant="ghost" size="icon-sm" aria-label="编辑" onClick={() => onEdit(c)}>
                  <Icon name="pen-line" size={14} />
                </Button>
                <Button
                  variant="ghost"
                  size="icon-sm"
                  aria-label="删除"
                  onClick={() => setDeleteTarget(c)}
                >
                  <Icon name="trash-2" size={14} />
                </Button>
              </div>
            </TableCell>
          </TableRow>
        ))}
      </TableFrame>

      <ConfirmDialog
        open={Boolean(deleteTarget)}
        onOpenChange={(o) => !o && setDeleteTarget(null)}
        destructive
        title={`删除分类「${deleteTarget?.name ?? ''}」`}
        description={
          <>
            分类本身会被删除。
            {deleteTarget && (deleteTarget.serverCount ?? 0) > 0 ? (
              <>
                {' '}
                属于它的 <b>{deleteTarget.serverCount} 个 Server</b> 会转为「未分类」，工具名前缀随之
                变成 <code className="code-chip">uncategorized</code> —— 正在按旧名调用这些工具的
                AI 客户端会找不到它们。
              </>
            ) : null}
            {' '}此操作不可撤销。
          </>
        }
        confirmText="确认删除"
        pending={mutations.remove.isPending}
        onConfirm={confirmDelete}
      />
    </>
  )
}
