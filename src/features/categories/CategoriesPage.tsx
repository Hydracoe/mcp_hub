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

/**
 * 分类。
 *
 * 分类不是给界面分类用的，它是**对外工具名的第一段**：
 *
 * ```
 * <分类英文名>__<serverKey>__<工具名>
 * ```
 *
 * 三段里 serverKey 有全局唯一约束、一个 server 又只属于一个分类，
 * 所以整名按构造就不会重复 —— 不需要「事后消解」命名冲突，
 * 也就不需要在这一页上摆一个冲突检测面板。
 */
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
      ready={overview?.ready}
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
        {/* 外壳 / 状态位 / 页脚注释统一收进 TableFrame：空表也保留表头。 */}
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
                  对外工具名 = <code className="code-chip">&lt;英文名&gt;__&lt;serverKey&gt;__&lt;工具名&gt;</code>
                  ；前缀段的唯一性由 serverKey 保证，英文名可以重复。
                </span>
                <span>删除分类不会删 Server，只把它们转为未分类。</span>
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
