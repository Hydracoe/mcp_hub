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

/** 分类列表。「英文名」（slug）用于列表与筛选，不进对外工具名。
 *  存量分类可能没有英文名，这类行要就地标出——筛选参数里它会缺位。 */
export function CategoriesTable({ items, frame, mutations, onEdit }: CategoriesTableProps) {
  const [deleteTarget, setDeleteTarget] = useState<Category | null>(null)

  const confirmDelete = async () => {
    if (!deleteTarget) return
    try {
      await mutations.remove.mutateAsync(deleteTarget.id)
      // 能走到这里说明分类下已无 Server（有 Server 后端会拒绝删除）。
      toastSuccess('已删除分类')
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
                  title="未设英文名（slug）：检索与按分类筛选时会缺这一段标识"
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
            {deleteTarget && (deleteTarget.serverCount ?? 0) > 0 ? (
              <>
                该分类下还有 <b>{deleteTarget.serverCount} 个 Server</b>
                ，删除会被拒绝（409）。请先把这些 Server 移到其他分类或删除，再回来删分类。
              </>
            ) : (
              <>该分类下没有 Server，可以安全删除。分类不进对外工具名，删除不影响任何工具名。</>
            )}
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
