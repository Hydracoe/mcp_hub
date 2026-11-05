import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'
import type { Transport } from '@/lib/api/types'

import { Callout, FieldHint, NumberField, Row } from './primitives'
import type { ServerFormState } from './form-model'
import type { FieldErrors } from './validation'

interface Props {
  f: ServerFormState
  set: <K extends keyof ServerFormState>(k: K, v: ServerFormState[K]) => void
  errs: FieldErrors
  keyError: string
}

/** Step 1：名称、Key 与传输方式。远程走 URL，stdio 走 Command/Args/Env。 */
export function Step1Transport({ f, set, errs, keyError }: Props) {
  const isRemote = f.transport !== 'stdio'
  const required = <span className="text-error-strong">*</span>

  return (
    <>
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
        <div>
          <Label>Name {required}</Label>
          <Input
            value={f.name}
            onChange={(e) => set('name', e.target.value)}
            placeholder="例如：Web Search API"
            aria-invalid={Boolean(errs.name)}
          />
          <FieldHint message={errs.name} fallback="仅展示名，工具前缀取自 Key" />
        </div>

        <div>
          <Label>Key {required}</Label>
          <Input
            value={f.key}
            onChange={(e) => set('key', e.target.value)}
            placeholder="例如：web-search"
            className="font-mono"
            aria-invalid={Boolean(keyError) || Boolean(errs.key)}
          />
          {/* key_taken 来自服务端，优先于本地格式校验展示。 */}
          <FieldHint
            message={keyError || errs.key}
            fallback="工具前缀，唯一约束；重复返回 key_taken"
          />
        </div>
      </div>

      <Row label="Enabled" hint="创建后立即进入聚合列表">
        <Switch checked={f.enabled} onCheckedChange={(v) => set('enabled', v)} />
      </Row>

      <div>
        <Label>Transport Type {required}</Label>
        <Select value={f.transport} onValueChange={(v) => set('transport', v as Transport)}>
          <SelectTrigger className="w-full max-w-70">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="streamable_http">streamable_http</SelectItem>
            <SelectItem value="sse">sse</SelectItem>
            <SelectItem value="stdio">stdio</SelectItem>
          </SelectContent>
        </Select>
        <p className="form-hint">切换类型会替换下方字段组</p>
      </div>

      {isRemote ? (
        <>
          <div>
            <Label>URL {required}</Label>
            <Input
              value={f.url}
              onChange={(e) => set('url', e.target.value)}
              placeholder="https://api.example.com/mcp"
              className="font-mono"
              aria-invalid={Boolean(errs.url)}
            />
            <FieldHint message={errs.url} />
          </div>

          {f.transport === 'streamable_http' ? (
            <>
              <div>
                <Label>Protocol Version</Label>
                <Input
                  value={f.protocolVersion}
                  onChange={(e) => set('protocolVersion', e.target.value)}
                  placeholder="2025-11-25"
                  className="font-mono"
                />
              </div>
              <Row label="Legacy Only" hint="强制旧版协议（确认目标只支持旧版时打开）">
                <Switch checked={f.legacyOnly} onCheckedChange={(v) => set('legacyOnly', v)} />
              </Row>
            </>
          ) : (
            <p className="form-hint">旧版远程传输。发消息时实时取凭据，换密钥基本无感。</p>
          )}
        </>
      ) : (
        <>
          <div>
            <Label>Command {required}</Label>
            <Input
              value={f.command}
              onChange={(e) => set('command', e.target.value)}
              placeholder="npx"
              className="font-mono"
              aria-invalid={Boolean(errs.command)}
            />
            <FieldHint message={errs.command} />
          </div>

          <div>
            <Label>Args</Label>
            <Textarea
              rows={3}
              value={f.argsText}
              onChange={(e) => set('argsText', e.target.value)}
              placeholder={'-y\n@modelcontextprotocol/server-filesystem\n/tmp'}
              className="font-mono text-[12px]"
            />
            <p className="form-hint">每行一个参数，保存为 JSON 数组</p>
          </div>

          <div>
            <Label>Env</Label>
            <Textarea
              rows={2}
              value={f.envText}
              onChange={(e) => set('envText', e.target.value)}
              placeholder={'KEY=value'}
              className="font-mono text-[12px]"
            />
            <p className="form-hint">每行 KEY=value 或 KEY，保存为字符串数组</p>
          </div>

          <Callout tone="warning">
            stdio 凭据在子进程环境变量里：改密钥必须重建连接，且 stdio 不支持密钥池。
          </Callout>
        </>
      )}

      <div>
        <Label>Timeout</Label>
        <NumberField
          unit="ms"
          value={f.timeoutMs}
          onChange={(e) => set('timeoutMs', Number(e.target.value))}
          aria-invalid={Boolean(errs.timeoutMs)}
          className="max-w-70"
        />
        <FieldHint
          message={errs.timeoutMs}
          fallback="默认 30s。单个 server 卡死不影响其他 server"
        />
      </div>
    </>
  )
}
