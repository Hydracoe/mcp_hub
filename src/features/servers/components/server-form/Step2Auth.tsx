import { Combobox } from '@/components/common/Combobox'
import { Button } from '@/components/ui/button'
import { Icon } from '@/components/common/Icon'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import type { Category } from '@/lib/api/types'

import { Callout, FieldHint, Row, SegmentedToggle } from './primitives'
import type { ServerFormState } from './form-model'
import type { FieldErrors } from './validation'

interface Props {
  f: ServerFormState
  set: <K extends keyof ServerFormState>(k: K, v: ServerFormState[K]) => void
  errs: FieldErrors
  categories: readonly Category[]
}

const NEEDS_KEY_OPTIONS = [
  { value: 'yes', label: '需要' },
  { value: 'no', label: '不需要' },
] as const

/** Step 2：鉴权方式、分类与展示用的元信息。 */
export function Step2Auth({ f, set, errs, categories }: Props) {
  const isRemote = f.transport !== 'stdio'
  const showHeaders = isRemote && f.needsKey

  const updateHeader = (i: number, patch: Partial<{ k: string; v: string }>) =>
    set(
      'headerRows',
      f.headerRows.map((r, idx) => (idx === i ? { ...r, ...patch } : r)),
    )

  return (
    <>
      {isRemote ? (
        <>
          <Row
            label="是否需要 API Key"
            hint="直连下游（无需鉴权）时选「不需要」，凭据字段整体不出现"
          >
            <SegmentedToggle
              aria-label="是否需要 API Key"
              value={f.needsKey ? 'yes' : 'no'}
              options={NEEDS_KEY_OPTIONS}
              onChange={(v) => set('needsKey', v === 'yes')}
            />
          </Row>

          {showHeaders ? (
            <div className="space-y-2">
              <Label>Headers</Label>
              {f.headerRows.map((r, i) => (
                // 行没有稳定 id（用户可以加空行），索引即身份。
                <div key={i} className="flex items-center gap-2">
                  <Input
                    value={r.k}
                    onChange={(e) => updateHeader(i, { k: e.target.value })}
                    placeholder="Authorization"
                    className="flex-1 font-mono text-[12px]"
                  />
                  <Input
                    value={r.v}
                    onChange={(e) => updateHeader(i, { v: e.target.value })}
                    placeholder="Bearer sk-…"
                    className="flex-1 font-mono text-[12px]"
                  />
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    aria-label="删除 Header"
                    onClick={() =>
                      set(
                        'headerRows',
                        f.headerRows.filter((_, idx) => idx !== i),
                      )
                    }
                  >
                    <Icon name="trash-2" size={14} />
                  </Button>
                </div>
              ))}
              <Button
                variant="outline"
                size="sm"
                onClick={() => set('headerRows', [...f.headerRows, { k: '', v: '' }])}
              >
                <Icon name="plus" size={13} /> 添加 Header
              </Button>
              <FieldHint message={errs.headers} />
            </div>
          ) : (
            <Callout>
              直连 Server：网关不注入任何凭据，密钥池禁用。之后可随时切换。
            </Callout>
          )}
        </>
      ) : (
        <Callout>stdio 下游不支持密钥池，凭据随子进程环境变量传入，此处无需配置。</Callout>
      )}

      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
        <div>
          <Label>
            分类 <span className="text-error-strong">*</span>
          </Label>
          {/* 分类数随 Server 增长，走带搜索的 Combobox */}
          <Combobox
            value={f.categoryId}
            onChange={(v) => set('categoryId', v)}
            options={categories.map((c) => ({ value: c.id, label: c.name }))}
            className="mt-1.5 w-full"
            placeholder="请选择分类"
            searchPlaceholder="搜索分类…"
            emptyText="还没有分类"
            aria-label="分类"
          />
          <FieldHint message={errs.categoryId} fallback="分类决定工具名前缀段与检索组织，必选" />
        </div>

        <div>
          <Label>标签 (逗号分隔)</Label>
          <Input
            value={f.tagsText}
            onChange={(e) => set('tagsText', e.target.value)}
            placeholder="search, web"
          />
        </div>
      </div>

      <div>
        <Label>描述</Label>
        <Textarea
          rows={2}
          value={f.description}
          onChange={(e) => set('description', e.target.value)}
          placeholder="选填"
        />
      </div>
    </>
  )
}
