import { useState } from 'react'

import { Button } from '@/components/ui/button'
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
import { Icon } from '@/components/common/Icon'
import { Badge } from '@/components/ui/badge'
import { toastError, toastSuccess } from '@/lib/toast'
import type { CredentialView } from '@/lib/api/types'
import type { useCredentialMutations } from '@/lib/api/queries'

type Mutations = ReturnType<typeof useCredentialMutations>
type Scheme = 'bearer' | 'x-api-key' | 'header'

interface Props {
  open: boolean
  onOpenChange: (open: boolean) => void
  mutations: Mutations
}

/** 批量粘贴密钥。不显示 serverKey（归属由页面 ServerPicker 表明）；
 *  补上「一行一把」的格式示例与 header 展开预览，密钥一次性贴错只能重来。 */
export function CredentialImportDialog({ open, onOpenChange, mutations }: Props) {
  const [raw, setRaw] = useState('')
  const [scheme, setScheme] = useState<Scheme>('bearer')
  const [headerName, setHeaderName] = useState('')
  const [rpmLimit, setRpmLimit] = useState('')
  const [result, setResult] = useState<{ created: number; items: CredentialView[] } | null>(null)

  const lines = raw
    .split('\n')
    .map((l) => l.trim())
    .filter(Boolean)
  const valid = lines.length > 0 && (scheme !== 'header' || headerName.trim() !== '')

  const headerPreview = scheme === 'bearer' ? 'Authorization: Bearer <值>' : scheme === 'x-api-key' ? 'x-api-key: <值>' : `${headerName || '<Header 名>'}: <值>`

  const submit = () => {
    if (!valid) return
    setResult(null)
    mutations.importLines.mutate(
      {
        lines,
        authScheme: scheme,
        headerName: scheme === 'header' ? headerName.trim() : undefined,
        rpmLimit: rpmLimit ? Number(rpmLimit) : undefined,
      },
      {
        onSuccess: (data) => {
          setResult(data)
          toastSuccess('导入完成', `新增 ${data.created} 把密钥`)
        },
        onError: (e) => toastError(e, '密钥导入失败'),
      },
    )
  }

  const close = (next: boolean) => {
    if (mutations.importLines.isPending) return
    if (!next) {
      setRaw('')
      setResult(null)
    }
    onOpenChange(next)
  }

  return (
    <FormDialog
      open={open}
      onOpenChange={close}
      size="lg"
      icon="upload"
      title="批量导入密钥"
      description="每行一把：label=value 或直接 value；下方预览它的 header 展开方式"
      pending={mutations.importLines.isPending}
      /* 提交按钮要额外受「已识别的行是否合法」约束，所以整块替换。 */
      footerEnd={
        <Button
          variant="primary"
          onClick={submit}
          disabled={!valid || mutations.importLines.isPending}
        >
          {mutations.importLines.isPending ? <Icon name="refresh-cw" size={13} className="animate-spin" /> : null}
          导入 {lines.length > 0 ? `${lines.length} 把` : ''}
        </Button>
      }
    >
      <div className="space-y-1.5">
        <Label htmlFor="cred-import-lines">密钥行（一行一把）</Label>
        <Textarea
          id="cred-import-lines"
          value={raw}
          onChange={(e) => setRaw(e.target.value)}
          rows={7}
          spellCheck={false}
          className="font-mono text-[12px]"
          placeholder={'主账号=sk-xxxxxxxx\n备用账号=sk-yyyyyyyy\nsk-zzzzzzzz'}
        />
        <p className="form-hint">
          支持两种写法：<code className="code-chip">名称=值</code> 或直接给值（自动命名）。已识别{' '}
          <span className="font-mono tabular-nums text-foreground">{lines.length}</span> 行。
        </p>
      </div>

      <div className="grid grid-cols-2 gap-4">
        <div className="space-y-1.5">
          <Label>鉴权方式</Label>
          <Select value={scheme} onValueChange={(v) => setScheme(v as Scheme)}>
            <SelectTrigger className="w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="bearer">bearer</SelectItem>
              <SelectItem value="x-api-key">x-api-key</SelectItem>
              <SelectItem value="header">header（自定义头名）</SelectItem>
            </SelectContent>
          </Select>
        </div>
        {scheme === 'header' ? (
          <div className="space-y-1.5">
            <Label htmlFor="cred-import-header">Header 名</Label>
            <Input
              id="cred-import-header"
              value={headerName}
              onChange={(e) => setHeaderName(e.target.value)}
              placeholder="X-Api-Token"
              className="font-mono"
            />
          </div>
        ) : null}
      </div>

      <div className="rounded-lg border border-separator bg-fill-tertiary px-3 py-2 text-[11.5px] text-muted-foreground">
        每把密钥会被展开成 <code className="code-chip">{headerPreview}</code>
      </div>

      <div className="max-w-xs space-y-1.5">
        <Label htmlFor="cred-import-rpm">RPM 上限</Label>
        <Input
          id="cred-import-rpm"
          type="number"
          inputMode="numeric"
          value={rpmLimit}
          onChange={(e) => setRpmLimit(e.target.value)}
          placeholder="留空 = 不限"
        />
      </div>

      {result ? (
        <div className="rounded-lg border border-separator">
          <div className="flex items-center gap-2 border-b border-separator px-3 py-2 text-[12px]">
            <Badge variant="success">新增 {result.created}</Badge>
            <span className="text-muted-foreground">密钥值不会回显，请以指纹核对。</span>
          </div>
          <ul className="max-h-44 divide-y divide-separator overflow-y-auto">
            {result.items.map((c) => (
              <li key={c.id} className="flex items-center gap-3 px-3 py-1.5 text-[12px]">
                <span className="text-foreground">{c.label}</span>
                <span className="font-mono text-[11px] text-muted-foreground">
                  {c.fingerprint ? `···${c.fingerprint}` : '—'}
                </span>
                <span className="ml-auto font-mono text-[11px] text-muted-foreground">
                  {c.headerKeys?.join(', ') ?? '—'}
                </span>
              </li>
            ))}
          </ul>
        </div>
      ) : null}
    </FormDialog>
  )
}
