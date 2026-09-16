import type { Dispatch, SetStateAction } from 'react'

import { Icon } from '@/components/common/Icon'
import { Label } from '@/components/ui/label'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { Alert, AlertDescription } from '@/components/ui/alert'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import type { AuthMode } from '@/lib/api/types'

import { KeyValueEditor } from './FormBits'
import type { ServerFormState } from '../ServerForm'

export interface TransportFieldsProps {
  form: ServerFormState
  setForm: Dispatch<SetStateAction<ServerFormState>>
}

export function TransportFields({ form, setForm }: TransportFieldsProps) {
  const update = (patch: Partial<ServerFormState>) => setForm((prev) => ({ ...prev, ...patch }))

  if (form.transport === 'stdio') {
    return (
      <div className="space-y-4">
        <div>
          <Label htmlFor="tf-cmd">Command</Label>
          <Input
            id="tf-cmd"
            className="font-mono"
            value={form.command}
            onChange={(e) => update({ command: e.target.value })}
            placeholder="npx"
          />
        </div>
        <div>
          <Label htmlFor="tf-args">Args</Label>
          <Textarea
            id="tf-args"
            rows={3}
            className="font-mono"
            value={form.argsText}
            onChange={(e) => update({ argsText: e.target.value })}
            placeholder={'-y\n@modelcontextprotocol/server-filesystem\n/tmp'}
          />
          {/* ⚠ 设计稿原版 Args 用单行输入；这里改为每行一个参数，保存为 JSON 数组 */}
          <p className="form-hint">每行一个参数，保存为 JSON 数组</p>
        </div>
        <div>
          <Label>Env</Label>
          <KeyValueEditor
            rows={form.env}
            onChange={(env) => update({ env })}
            keyPlaceholder="KEY"
            valuePlaceholder="value"
            addLabel="添加环境变量"
          />
        </div>
        <Alert variant="warning">
          <Icon name="triangle-alert" size={15} />
          <AlertDescription>
            stdio 的凭据在子进程环境变量里：改密钥必须重建连接（在飞调用走优雅重建）；且
            <span className="mx-1 font-mono">stdio</span> 不支持密钥池——添加密钥会返回
            <span className="mx-1 font-mono">400 key_pool_unsupported</span>。
          </AlertDescription>
        </Alert>
      </div>
    )
  }

  // 远程传输：sse / streamable_http
  return (
    <div className="space-y-4">
      <div>
        <Label htmlFor="tf-url">URL</Label>
        <Input
          id="tf-url"
          className="font-mono"
          value={form.url}
          onChange={(e) => update({ url: e.target.value })}
          placeholder="https://api.example.com/mcp"
        />
      </div>

      <div className="flex items-center justify-between gap-3 flex-wrap">
        <div>
          <Label className="mb-0">是否需要 API Key</Label>
          <p className="form-hint">直连下游（无需鉴权）时选「不需要」</p>
        </div>
        <Select value={form.authMode} onValueChange={(v) => update({ authMode: v as AuthMode })}>
          <SelectTrigger className="w-37.5">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="none">不需要鉴权</SelectItem>
            <SelectItem value="header">需要鉴权</SelectItem>
          </SelectContent>
        </Select>
      </div>

      {form.authMode === 'header' ? (
        <div className="space-y-3">
          <div className="max-w-xs">
            <Label htmlFor="tf-auth-header">鉴权头名</Label>
            <Input
              id="tf-auth-header"
              className="font-mono"
              value={form.authHeaderName}
              onChange={(e) => update({ authHeaderName: e.target.value })}
              placeholder="Authorization"
            />
          </div>
          <KeyValueEditor
            rows={form.headers}
            onChange={(headers) => update({ headers })}
            keyPlaceholder="Header 名"
            valuePlaceholder="Bearer sk-…"
            addLabel="添加 Header"
          />
          {/* ⚠ 设计稿原版把「提交 *** 表示沿用原值」只写在 hint 里；回显值仍是 *** 时保存不回传，见 buildServerRequest */}
          <p className="form-hint">
            <Icon name="info" size={13} className="mr-1 inline-block align-[-2px]" />
            未改的 Header 值回显为 <span className="font-mono">***</span>，保存时沿用原值，不会把
            <span className="font-mono">***</span> 当真值提交。
          </p>
        </div>
      ) : (
        <Alert variant="warning">
          <Icon name="triangle-alert" size={15} />
          <AlertDescription>
            直连 Server：网关不向下游注入任何凭据，密钥池对它禁用。之后想改成需要鉴权，可随时在列表编辑。
          </AlertDescription>
        </Alert>
      )}
    </div>
  )
}
