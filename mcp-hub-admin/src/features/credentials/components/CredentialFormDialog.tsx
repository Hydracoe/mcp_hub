import { useEffect, useState } from 'react'

import { FormDialog } from '@/components/common/FormDialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { toastError, toastSuccess } from '@/lib/toast'
import type { CredentialRequest, CredentialView } from '@/lib/api/types'
import type { useCredentialMutations } from '@/lib/api/queries'

type Mutations = ReturnType<typeof useCredentialMutations>
type Scheme = 'bearer' | 'x-api-key' | 'header'

interface CredentialFormDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  initial: CredentialView | null
  mutations: Mutations
  /** 当前 server 已有的密钥：提交前查重，同一 server 内标签不允许重复（与后端唯一索引同口径）。 */
  existing?: CredentialView[]
}

/** 新增/编辑密钥表单。不在弹框里显示 server（归属由页面级 ServerPicker 决定）；
 *  CredentialRequest 无 enabled 字段，启用/停用走独立的 setEnabled 接口。 */
export function CredentialFormDialog({
  open,
  onOpenChange,
  initial,
  mutations,
  existing = [],
}: CredentialFormDialogProps) {
  const [label, setLabel] = useState('')
  const [mode, setMode] = useState<'value' | 'headers'>('value')
  const [value, setValue] = useState('')
  const [authScheme, setAuthScheme] = useState<Scheme>('bearer')
  const [headerName, setHeaderName] = useState('')
  const [headersText, setHeadersText] = useState('')
  const [rpmLimit, setRpmLimit] = useState('')
  const [error, setError] = useState('')

  useEffect(() => {
    if (!open) return
    setLabel(initial?.label ?? '')
    setMode('value')
    setValue('')
    setAuthScheme('bearer')
    setHeaderName('')
    setHeadersText('')
    setRpmLimit('')
    setError('')
  }, [open, initial])

  const isEdit = Boolean(initial)

  const buildRequest = (): CredentialRequest | null => {
    if (!label.trim()) {
      setError('请填写 Label')
      return null
    }
    // 同一 server 内标签唯一（后端有部分唯一索引兜底，这里提前拦下给出内联提示）。
    // 编辑时排除自身；比对用 trim 后的值，与提交值一致。
    const name = label.trim()
    const dup = existing.find((c) => c.id !== initial?.id && c.label === name)
    if (dup) {
      setError(`该 Server 下已有同名标签「${name}」，密钥标签不能重复`)
      return null
    }
    const req: CredentialRequest = { label: name }
    if (mode === 'value') {
      if (value) {
        req.value = value
        req.authScheme = authScheme
        if (authScheme === 'header') {
          if (!headerName.trim()) {
            setError('请填写 Header Name')
            return null
          }
          req.headerName = headerName.trim()
        }
      }
    } else {
      if (headersText.trim()) {
        try {
          req.headers = JSON.parse(headersText)
        } catch {
          setError('Headers 不是合法 JSON')
          return null
        }
      } else if (!isEdit) {
        setError('请填写 Headers')
        return null
      }
    }
    /* 限额须为非负整数：负数会绕过 x>0 直接锁死调度，且 number input 允许键入 e/+/-，不能只靠 min。 */
    if (rpmLimit.trim()) {
      const n = Number(rpmLimit)
      if (!Number.isInteger(n) || n < 0) {
        setError('rpmLimit 必须是非负整数')
        return null
      }
      req.rpmLimit = n
    }
    return req
  }

  const submit = async () => {
    const req = buildRequest()
    if (!req) return
    try {
      if (isEdit && initial) {
        await mutations.update.mutateAsync({ cid: initial.id, body: req })
        toastSuccess('已更新密钥')
      } else {
        await mutations.create.mutateAsync(req)
        toastSuccess('已新增密钥')
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
      title={isEdit ? '编辑密钥' : '新增密钥'}
      description="密钥只进不出，编辑时留空值即沿用原值"
      pending={pending}
      submitText={isEdit ? '保存' : '新增'}
      onSubmit={submit}
      error={error}
    >
      <div>
        <Label>Label</Label>
        <Input
          value={label}
          onChange={(e) => setLabel(e.target.value)}
          placeholder="例如：主号 / 备用-1"
          className="mt-1.5"
        />
      </div>

      <div className="tab-toggle">
        <button type="button" data-active={mode === 'value'} onClick={() => setMode('value')}>
          值 + 鉴权方案
        </button>
        <button type="button" data-active={mode === 'headers'} onClick={() => setMode('headers')}>
          直接给 headers JSON
        </button>
      </div>

      {mode === 'value' ? (
        <div className="space-y-4">
          <div>
            <Label>Value</Label>
            <Input
              type="password"
              value={value}
              onChange={(e) => setValue(e.target.value)}
              placeholder={isEdit ? '***（沿用原值）' : 'sk_live_…'}
              className="mt-1.5 font-mono"
              autoComplete="off"
            />
          </div>
          <div>
            <Label>Auth Scheme</Label>
            <Select value={authScheme} onValueChange={(v) => setAuthScheme(v as Scheme)}>
            <SelectTrigger className="mt-1.5 w-full">
              <SelectValue />
            </SelectTrigger>
              <SelectContent>
                <SelectItem value="bearer">bearer — Authorization: Bearer</SelectItem>
                <SelectItem value="x-api-key">x-api-key — X-API-Key</SelectItem>
                <SelectItem value="header">header — 自定义头名</SelectItem>
              </SelectContent>
            </Select>
          </div>
          {authScheme === 'header' ? (
            <div>
              <Label>Header Name</Label>
              <Input
                value={headerName}
                onChange={(e) => setHeaderName(e.target.value)}
                placeholder="例如：X-Custom-Token"
                className="mt-1.5 w-[280px] font-mono"
              />
            </div>
          ) : null}
        </div>
      ) : (
        <div>
          <Label>Headers JSON</Label>
          <Textarea
            value={headersText}
            onChange={(e) => setHeadersText(e.target.value)}
            rows={4}
            placeholder={'{\n  "Authorization": "Bearer sk_live_…"\n}'}
            className="mt-1.5 font-mono"
          />
        </div>
      )}

      <div>
        <Label>rpmLimit</Label>
        <Input
          type="number"
          min={0}
          step={1}
          value={rpmLimit}
          onChange={(e) => setRpmLimit(e.target.value)}
          placeholder="不限"
          className="mt-1.5"
        />
      </div>

    </FormDialog>
  )
}
