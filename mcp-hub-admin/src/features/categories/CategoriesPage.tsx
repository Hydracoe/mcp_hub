import { useState } from 'react'

import { Button } from '@/components/ui/button'
import { Icon } from '@/components/common/Icon'
import { PageLayout } from '@/components/layout/PageLayout'
import { Section } from '@/components/common/Section'
import { EmptyState } from '@/components/common/States'
import { useCategories, useCategoryMutations, useOverview } from '@/lib/api/queries'
import { formatInt } from '@/lib/format'
import type { Category } from '@/lib/api/types'

import { CategoriesTable } from './components/CategoriesTable'
import { CategoryFormDialog } from './components/CategoryFormDialog'

/** 分类只做组织与检索，不进对外工具名（<serverKey>__<工具名>）；
 *  分类下还有 Server 时禁止删除。 */
export function CategoriesPage() {
  const { data: overview } = useOverview()
  const { data, isLoading, error, refetch } = useCategories()
  const mutations = useCategoryMutations()

  const [formOpen, setFormOpen] = useState(false)
  const [editing, setEditing] = useState<Category | null>(null)

  const items = data?.items ?? []
  const missingSlug = items.filter((c) => !c.slug).length

  const openCreate = () => {
    setEditing(null)
    setFormOpen(true)
  }
  const openEdit = (c: Category) => {
    setEditing(c)
    setFormOpen(true)
  }

  return (
    <PageLayout
      title="分类"
      version={overview?.version}
      actions={
        <Button variant="primary" size="sm" onClick={openCreate}>
          <Icon name="plus" size={13} />
          新增分类
        </Button>
      }
    >
      <Section
        title="全部分类"
        hint={
          <span className="text-[11.5px] text-muted-foreground">
            共 {formatInt(items.length)} 个
            {missingSlug > 0 ? ` · ${missingSlug} 个缺英文名` : ''}
          </span>
        }
        actions={
          <Button variant="ghost" size="icon-sm" aria-label="刷新" onClick={() => void refetch()}>
            <Icon name="refresh-cw" size={14} />
          </Button>
        }
      >
        <CategoriesTable
          items={items}
          mutations={mutations}
          onEdit={openEdit}
          frame={{
            loading: isLoading,
            error: error
              ? { title: '分类加载失败', error, onRetry: () => void refetch() }
              : undefined,
            isEmpty: items.length === 0,
            empty: (
              <EmptyState
                icon="folder"
                title="还没有分类"
                description="分类决定工具的对外名前缀。不建分类也能用：未分类的 server 前缀固定为 uncategorized。"
                action={
                  <Button variant="primary" size="sm" onClick={openCreate}>
                    新增分类
                  </Button>
                }
              />
            ),
            footer: (
              <div className="flex flex-wrap items-center gap-x-4 gap-y-1 border-t border-separator px-4 py-2.5 text-[11.5px] text-muted-foreground">
                <span>
                  对外工具名 = <code className="code-chip">&lt;serverKey&gt;__&lt;工具名&gt;</code>
                  ；分类不进工具名，新增、改名或删除分类都不会改动已有工具名。
                </span>
                <span>分类下还有 Server 时不能删除，需先把这些 Server 移到其他分类。</span>
              </div>
            ),
          }}
        />
      </Section>

      <CategoryFormDialog
        open={formOpen}
        onOpenChange={setFormOpen}
        initial={editing}
        mutations={mutations}
      />
    </PageLayout>
  )
}
