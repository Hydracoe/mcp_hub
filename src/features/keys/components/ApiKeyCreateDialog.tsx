import { useEffect, useState } from 'react'

import { FormDialog } from '@/components/common/FormDialog'
import { CopyButton } from '@/components/common/CopyButton'
import { Icon } from '@/components/common/Icon'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { ApiError } from '@/lib/api/client'
import { formatDateTime } from '@/lib/format'
import type { ApiKeyCreateResponse } from '@/lib/api/types'
import type { useApiKeyMutations } from '@/lib/api/queries'

type Mutations = ReturnType<typeof useApiKeyMutations>

interface ApiKeyCreateDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  mutations: Mutations
}

/**
 * 新建接入密钥。
 *
 * 两段式：填名字 → 交付明文。之所以不合成一段，是因为明文
 * **不可重放**（库里只有 sha256），而"创建成功"的 toast 一闪就过去了，
 * 用户很容易在看清那串字符之前把它弄丢。
 *
 * 第二段刻意不给「取消」：那时候关掉弹框不是"放弃操作"，
 * 留一个取消按钮会让人以为这次创建还能撤回。
 */
export function ApiKeyCreateDialog({ open, onOpenChange, mutations }: ApiKeyCreateDialogProps) {
  const [name, setName] = useState('')
  const [error, setError] = useState('')
  /**
   * 明文只活在这次弹框会话的内存里：不写 localStorage、不进 URL、
   * 也不交给 react-query 的缓存 —— 那会让明文一直留在内存里。
   */
  const [created, setCreated] = useState<ApiKeyCreateResponse | null>(null)

  useEffect(() => {
    if (!open) return
    setName('')
    setError('')
    setCreated(null)
  }, [open])

  const submit = async () => {
    const value = name.trim()
    if (!value) {
      setError('请填写名称')
      return
    }
    try {
      setCreated(await mutations.create.mutateAsync(value))
      setError('')
    } catch (e) {
      setError(e instanceof ApiError ? e.message : '创建失败，请重试')
    }
  }

  return (
    <FormDialog
      open={open}
      onOpenChange={onOpenChange}
      size="lg"
      icon={created ? 'shield-check' : 'key-round'}
      title={created ? '密钥已创建' : '新建接入密钥'}
      description={
        created ? null : '名称用来在调用日志里认出这个调用方，例如「Claude Desktop · 我的 Mac」。'
      }
      pending={!created && mutations.create.isPending}
      submitText={created ? '我已保存' : '创建'}
      hideCancel={Boolean(created)}
      onSubmit={created ? () => onOpenChange(false) : submit}
      error={error}
    >
      {created ? (
        <PlaintextDelivery created={created} />
      ) : (
        <div>
          <Label>名称</Label>
          <Input
            value={name}
            onChange={(e) => setName(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') void submit()
            }}
            placeholder="例如：Claude Desktop · 我的 Mac"
            className="mt-1.5"
            autoFocus
            autoComplete="off"
          />
          {/* 这一步的副作用要说在前面：建完第一把 key，端点就不再对匿名开放。 */}
          <p className="mt-2 text-[11.5px] text-muted-foreground">
            创建后 <code className="code-chip">/mcp</code> 立即开始要求鉴权：所有调用方都必须带上密钥，
            匿名请求会收到 401。
          </p>
        </div>
      )}
    </FormDialog>
  )
}

/** 第二段：交付明文。这一屏的唯一任务是让它被复制走。 */
function PlaintextDelivery({ created }: { created: ApiKeyCreateResponse }) {
  return (
    <div className="space-y-3">
      <div className="flex items-start gap-2.5 rounded-md border border-warning/30 bg-warning-surface px-3 py-2.5 text-[12.5px] text-foreground">
        <Icon name="alert-triangle" size={15} className="mt-0.5 shrink-0 text-warning-strong" />
        <span>
          这串密钥<b>只显示这一次</b>。网关只保存它的哈希，关掉弹框后就再也取不回来 ——
          丢了只能删掉重建。
        </span>
      </div>

      <div className="flex items-center gap-2">
        <code className="code-chip min-w-0 flex-1 truncate font-mono">{created.key}</code>
        <CopyButton value={created.key} label="复制密钥" />
      </div>

      <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5 text-[12.5px]">
        <dt className="text-muted-foreground">名称</dt>
        <dd className="text-foreground">{created.name}</dd>
        <dt className="text-muted-foreground">前缀</dt>
        <dd className="font-mono text-foreground">{created.prefix}</dd>
        <dt className="text-muted-foreground">创建时间</dt>
        <dd className="text-foreground">{formatDateTime(created.createdAt)}</dd>
      </dl>

      <p className="text-[11.5px] text-muted-foreground">
        客户端带上它：<code className="code-chip">Authorization: Bearer &lt;密钥&gt;</code> 或{' '}
        <code className="code-chip">X-API-Key: &lt;密钥&gt;</code>。
      </p>
    </div>
  )
}
