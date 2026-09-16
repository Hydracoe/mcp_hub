import { useEffect, useMemo, useState } from 'react'

import { FormDialog } from '@/components/common/FormDialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { toastError, toastSuccess } from '@/lib/toast'
import { MAX_CATEGORY_SLUG_LEN, normalizeCategorySlug, previewToolName } from '@/lib/slug'
import type { Category } from '@/lib/api/types'
import type { useCategoryMutations } from '@/lib/api/queries'

type Mutations = ReturnType<typeof useCategoryMutations>

interface CategoryFormDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** 编辑时传入原分类；新增时为 null。 */
  initial: Category | null
  mutations: Mutations
}

/**
 * 新增 / 编辑分类。
 *
 * 中文名与英文名都必须填，这是刻意的：中文名缺了界面无处显示，
 * 英文名缺了对外工具名的前缀段只能退化成随机 id（`k2f9x1q8__github__…`），
 * 而工具名是给 AI 看的标识符，它不可读会一路影响到检索与调用。
 *
 * 表单里实时预览最终工具名，是因为「英文名」这个字段的影响面
 * 不在本页——它改的是每个下游工具对外的名字。预览是让用户
 * 在按下保存之前就看见这一点。
 */
export function CategoryFormDialog({ open, onOpenChange, initial, mutations }: CategoryFormDialogProps) {
  const [name, setName] = useState('')
  const [slug, setSlug] = useState('')
  const [description, setDescription] = useState('')
  const [error, setError] = useState('')

  useEffect(() => {
    if (!open) return
    setName(initial?.name ?? '')
    setSlug(initial?.slug ?? '')
    setDescription(initial?.description ?? '')
    setError('')
  }, [open, initial])

  const isEdit = Boolean(initial)
  const normalized = useMemo(() => normalizeCategorySlug(slug), [slug])
  const preview = useMemo(() => previewToolName(slug), [slug])

  const submit = async () => {
    if (!name.trim()) {
      setError('中文名不能为空')
      return
    }
    if (!normalized) {
      setError('英文名不能为空，且至少包含一个字母或数字')
      return
    }
    setError('')
    const body = { name: name.trim(), slug: normalized, description }
    try {
      if (isEdit && initial) {
        await mutations.update.mutateAsync({ id: initial.id, body })
        toastSuccess('已保存分类', initial.slug !== normalized ? '该分类下工具的对外名已随之更新' : undefined)
      } else {
        await mutations.create.mutateAsync(body)
        toastSuccess('已创建分类')
      }
      onOpenChange(false)
    } catch (e) {
      toastError(e)
    }
  }

  const pending = mutations.create.isPending || mutations.update.isPending

  return (
    <FormDialog
      open={open}
      onOpenChange={onOpenChange}
      size="lg"
      title={isEdit ? '编辑分类' : '新增分类'}
      description={
        <>
          中文名用于展示，英文名进工具名 —— 对外工具名统一为{' '}
          <code className="code-chip">&lt;英文名&gt;__&lt;serverKey&gt;__&lt;工具名&gt;</code>
        </>
      }
      pending={pending}
      submitText={isEdit ? '保存' : '新增'}
      onSubmit={submit}
      error={error}
    >
      <div>
        <Label>
          中文名 <span className="text-error-strong">*</span>
        </Label>
        <Input
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder="例如：天气查询"
          className="mt-1.5"
        />
        <p className="mt-1.5 text-[11.5px] text-muted-foreground">
          分类列表、下拉选择与检索语料里显示这个。同名会建不进去。
        </p>
      </div>

      <div>
        <Label>
          英文名 <span className="text-error-strong">*</span>
        </Label>
        <Input
          value={slug}
          onChange={(e) => setSlug(e.target.value)}
          placeholder="例如：weather"
          className="mt-1.5 font-mono"
          autoComplete="off"
          maxLength={MAX_CATEGORY_SLUG_LEN * 2}
        />
        <p className="mt-1.5 text-[11.5px] leading-relaxed text-muted-foreground">
          会自动折成小写字母、数字与连字符（最长 {MAX_CATEGORY_SLUG_LEN} 个字符）。
          {slug !== normalized && normalized ? (
            <>
              {' '}
              将存为 <code className="code-chip">{normalized}</code>
            </>
          ) : null}
        </p>
      </div>

      {/* 预览：这个字段改的是工具对外的名字，影响面在别的页面上，
          所以必须在这里就让用户看见。 */}
      <div className="ui-card bg-fill-tertiary/60 p-3.5">
        <p className="text-[11.5px] font-medium text-muted-foreground">工具名预览</p>
        <p className="mt-1.5 font-mono text-[12.5px] text-foreground">{preview}</p>
        <p className="mt-1.5 text-[11.5px] leading-relaxed text-muted-foreground">
          中文名与英文名都不能与已有分类重复，重复会返回 409。
          对外工具名的唯一性由 <code className="code-chip">serverKey</code> 的全局唯一约束
          保证（一个 server 只属于一个分类），两个 server 永远不会撞名。
        </p>
      </div>

      <div>
        <Label>描述</Label>
        <Textarea
          value={description}
          onChange={(e) => setDescription(e.target.value)}
          rows={3}
          placeholder="这个分类装什么样的下游。中文名与描述都会进入检索语料，用于按意图匹配工具。"
          className="mt-1.5"
        />
      </div>
    </FormDialog>
  )
}
