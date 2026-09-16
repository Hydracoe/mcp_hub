import { useEffect, useState, type ReactNode } from 'react'
import { FormDialog } from '@/components/common/FormDialog'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { Checkbox } from '@/components/ui/checkbox'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Combobox } from '@/components/common/Combobox'
import { Icon } from '@/components/common/Icon'
import { useCreateServer, useCategories } from '@/lib/api/queries'
import { probeServerDraft } from '@/lib/api/endpoints'
import { toastError, toastSuccess } from '@/lib/toast'
import { ApiError } from '@/lib/api/client'
import type { AuthMode, ServerRequest, Transport } from '@/lib/api/types'

interface Props {
  open: boolean
  onOpenChange: (open: boolean) => void
}

interface FormState {
  name: string
  key: string
  enabled: boolean
  transport: Transport
  url: string
  command: string
  argsText: string
  envText: string
  protocolVersion: string
  legacyOnly: boolean
  timeoutMs: number
  needsKey: boolean
  headerRows: { k: string; v: string }[]
  estTokensPerCall: string
  description: string
  tagsText: string
  categoryId: string
  /** 提交创建时是否顺带 ?probe=1 刷新工具缓存（创建前已经干跑过一次）。 */
  probe: boolean

  /* 探活与暴露（Step 3）。 */
  probeStatus: 'idle' | 'probing' | 'ok' | 'fail'
  /** 探活拿到的下游工具清单。 */
  probeTools: { name: string; description?: string }[]
  /** 工具名 → 是否暴露。默认全选。 */
  probeSelected: Record<string, boolean>
  /** 探活失败时的诊断信息。 */
  probeFail: { kind?: string; hint?: string; detail?: string; stderr?: string } | null
  /** 探活时的配置签名；连接相关字段变了就要求重新探活。 */
  probeSig: string
  /** 探活失败时的兜底：跳过筛选，暴露全部工具。 */
  fallbackAll: boolean
}

const EMPTY: FormState = {
  name: '', key: '', enabled: true, transport: 'streamable_http',
  url: '', command: '', argsText: '', envText: '', protocolVersion: '', legacyOnly: false,
  timeoutMs: 30000, needsKey: true, headerRows: [{ k: '', v: '' }],
  estTokensPerCall: '', description: '', tagsText: '', categoryId: '', probe: true,
  probeStatus: 'idle', probeTools: [], probeSelected: {}, probeFail: null, probeSig: '',
  fallbackAll: false,
}

const STEPS = ['基础与传输', '鉴权与元信息', '探活与暴露'] as const

/**
 * 新增 Server 向导。
 *
 * 暴露流程是「先探活 → 拿 tools/list → 勾选哪些工具对外暴露」：
 * 工具名来自探活结果，而不是让用户对着文档手抄模式串——
 * 创建时还不知道下游有什么工具，凭空填 allow 名单必然写错。
 */
export function ServerFormDialog({ open, onOpenChange }: Props) {
  const [step, setStep] = useState(1)
  const [f, setF] = useState<FormState>(EMPTY)
  const [keyError, setKeyError] = useState('')
  const [errs, setErrs] = useState<Record<string, string | undefined>>({})
  const create = useCreateServer()
  const { data: cats } = useCategories()

  useEffect(() => {
    if (open) {
      setStep(1)
      setF(EMPTY)
      setKeyError('')
      setErrs({})
    }
  }, [open])

  const set = <K extends keyof FormState>(k: K, v: FormState[K]) => setF((p) => ({ ...p, [k]: v }))
  /** 字段改动即清除该字段的校验错误。 */
  const setAndClear = <K extends keyof FormState>(k: K, v: FormState[K]) => {
    setErrs((p) => (p[k as string] ? { ...p, [k as string]: undefined } : p))
    set(k, v)
  }

  /* ── 校验：必填项与参数类型，分步执行，错误挂在字段下面 ── */

  const KEY_RE = /^[a-z0-9][a-z0-9_-]*$/

  const validateStep1 = (): Record<string, string> => {
    const e: Record<string, string> = {}
    if (!f.name.trim()) e.name = '必填'
    if (!f.key.trim()) e.key = '必填'
    else if (!KEY_RE.test(f.key.trim())) e.key = '仅允许小写字母、数字、-、_，且以字母/数字开头'
    if (!(Number.isInteger(f.timeoutMs) && f.timeoutMs > 0)) e.timeoutMs = '必须是正整数（毫秒）'
    if (f.transport === 'stdio') {
      if (!f.command.trim()) e.command = 'stdio 传输必须填写 Command'
    } else {
      if (!f.url.trim()) e.url = '远程传输必须填写 URL'
      else {
        try {
          const u = new URL(f.url.trim())
          if (u.protocol !== 'http:' && u.protocol !== 'https:') throw new Error()
        } catch {
          e.url = '必须是合法的 http(s) 地址'
        }
      }
    }
    return e
  }

  const validateStep2 = (): Record<string, string> => {
    const e: Record<string, string> = {}
    if (!f.categoryId) e.categoryId = '必选：分类决定工具名前缀段'
    if (f.estTokensPerCall.trim()) {
      const n = Number(f.estTokensPerCall)
      if (!Number.isInteger(n) || n < 0) e.estTokensPerCall = '必须是非负整数'
    }
    if (f.transport !== 'stdio' && f.needsKey) {
      const hasBad = f.headerRows.some((r) => !r.k.trim() && r.v.trim())
      const hasGood = f.headerRows.some((r) => r.k.trim())
      if (!hasGood) e.headers = '至少填写一行 Header（名称必填）'
      else if (hasBad) e.headers = '有 Header 只填了值没填名称，请补全或删除'
    }
    return e
  }

  const goNext = () => {
    if (step === 1) {
      const e = validateStep1()
      setErrs(e)
      if (Object.keys(e).length) return
    }
    if (step === 2) {
      const e = validateStep2()
      setErrs(e)
      if (Object.keys(e).length) return
      // 进入探活步前：连接配置变过就把旧探活结果作废，避免拿陈旧清单勾选。
      if (f.probeSig && f.probeSig !== configSig()) {
        setF((p) => ({
          ...p,
          probeStatus: 'idle', probeTools: [], probeSelected: {},
          probeFail: null, probeSig: '', fallbackAll: false,
        }))
      }
    }
    setStep((s) => s + 1)
  }

  /** 影响探活结论的字段签名：变了，之前的探活结果与勾选就全部作废。 */
  const configSig = () =>
    JSON.stringify([
      f.key.trim(), f.transport, f.url.trim(), f.command.trim(), f.argsText, f.envText,
      f.protocolVersion.trim(), f.legacyOnly, f.timeoutMs, f.needsKey, f.headerRows,
    ])

  /** 探活请求体：只带连接相关字段，工具筛选尚未确定。 */
  const probeBody = (): ServerRequest => {
    const body: ServerRequest = {
      key: f.key.trim(),
      name: f.name.trim(),
      transport: f.transport,
      timeoutMs: Number(f.timeoutMs) || undefined,
    }
    if (f.transport === 'stdio') {
      body.command = f.command.trim()
      const args = f.argsText.split('\n').map((s) => s.trim()).filter(Boolean)
      if (args.length) body.args = args
      const env = f.envText.split('\n').map((s) => s.trim()).filter(Boolean)
      if (env.length) body.env = env
    } else {
      body.url = f.url.trim()
      if (f.protocolVersion.trim()) body.protocolVersion = f.protocolVersion.trim()
      if (f.transport === 'streamable_http') body.legacyOnly = f.legacyOnly
      const authMode: AuthMode = f.needsKey ? 'header' : 'none'
      body.authMode = authMode
      if (f.needsKey) {
        const headers = Object.fromEntries(f.headerRows.map((r) => [r.k.trim(), r.v]).filter(([k]) => k))
        if (Object.keys(headers).length) body.headers = headers
      }
    }
    return body
  }

  const buildBody = (): ServerRequest => {
    const body = probeBody()
    const selected = f.probeTools.filter((t) => f.probeSelected[t.name]).map((t) => t.name)
    // 只在「探活成功且没有全选」时收窄成 allow 名单；全选等价于不过滤。
    if (f.probeStatus === 'ok' && f.probeTools.length > 0 && selected.length < f.probeTools.length) {
      body.toolFilterMode = 'allow'
      body.toolFilterList = selected
    }
    if (f.estTokensPerCall.trim()) body.estTokensPerCall = Number(f.estTokensPerCall)
    if (f.description.trim()) body.description = f.description.trim()
    if (f.tagsText.trim()) body.tags = f.tagsText.split(',').map((s) => s.trim()).filter(Boolean)
    body.categoryId = f.categoryId
    return body
  }

  const runProbe = async () => {
    const e1 = validateStep1()
    const e2 = validateStep2()
    const all = { ...e1, ...e2 }
    setErrs(all)
    if (Object.keys(all).length) return toastError(new Error(Object.values(all)[0] as string))
    setKeyError('')
    set('probeStatus', 'probing')
    try {
      const res = await probeServerDraft(probeBody())
      if (res.ok && (res.tools?.length ?? 0) > 0) {
        setF((p) => ({
          ...p,
          probeStatus: 'ok',
          probeTools: res.tools ?? [],
          probeSelected: Object.fromEntries((res.tools ?? []).map((t) => [t.name, true])),
          probeFail: null,
          probeSig: configSig(),
        }))
      } else if (res.ok) {
        // 协商成功但下游一个工具都没有：按失败处理，勾选无从谈起。
        setF((p) => ({
          ...p,
          probeStatus: 'fail',
          probeFail: { kind: 'no_tools', detail: '下游没有声明任何工具', hint: '确认这是工具型 server 而不是资源型' },
          probeSig: configSig(),
        }))
      } else {
        setF((p) => ({
          ...p,
          probeStatus: 'fail',
          probeFail: { kind: res.kind, hint: res.hint, detail: res.detail, stderr: res.stderr },
          probeSig: configSig(),
        }))
      }
    } catch (e) {
      set('probeStatus', 'fail')
      set('probeFail', { kind: 'request_failed', detail: e instanceof Error ? e.message : String(e) })
    }
  }

  const submit = async () => {
    const e1 = validateStep1()
    const e2 = validateStep2()
    const all = { ...e1, ...e2 }
    setErrs(all)
    if (Object.keys(all).length) return toastError(new Error(Object.values(all)[0] as string))
    // 探活已成功但配置后来又改了：旧清单对应的可能已经不是这套连接。
    const stale = f.probeSig !== '' && f.probeSig !== configSig()
    if (stale) return toastError(new Error('连接配置已变化，请重新探活后再创建'))
    if (f.probeStatus === 'ok' && !f.fallbackAll) {
      const selected = f.probeTools.filter((t) => f.probeSelected[t.name]).length
      if (selected === 0) return toastError(new Error('至少勾选一个要暴露的工具'))
    }
    setKeyError('')
    try {
      await create.mutateAsync({ body: buildBody(), probe: f.probe })
      toastSuccess('Server 已创建', f.probe ? '已带探活结果' : undefined)
      onOpenChange(false)
    } catch (e) {
      // 缺口 D6：key_taken 回到 key 字段做行内错误，而不是只弹 toast
      if (e instanceof ApiError && e.code === 'key_taken') {
        setKeyError(e.message)
        setStep(1)
        return
      }
      toastError(e, '创建失败')
    }
  }

  return (
    <FormDialog
      open={open}
      onOpenChange={onOpenChange}
      size="xl"
      icon="plus"
      title="新增 Server"
      description={<span className="font-mono">POST /api/servers?probe=1</span>}
      pending={create.isPending}
      /* 三步向导：左「取消」由模板给，右侧「上一步 / 下一步 / 创建」自己排。 */
      footerExtra={
        step > 1 ? (
          <Button variant="outline" onClick={() => setStep((s) => s - 1)} disabled={create.isPending}>
            上一步
          </Button>
        ) : null
      }
      footerEnd={
        step < 3 ? (
          <Button variant="primary" onClick={goNext}>
            下一步
          </Button>
        ) : (
          <Button variant="primary" onClick={submit} disabled={create.isPending}>
            {create.isPending ? <Icon name="refresh-cw" size={14} className="animate-spin" /> : <Icon name="check" size={14} />}
            创建
          </Button>
        )
      }
    >
      <StepBar step={step} />

      {step === 1 ? (
        <Step1 f={f} set={setAndClear} errs={errs} keyError={keyError} />
      ) : step === 2 ? (
        <Step2 f={f} set={setAndClear} errs={errs} cats={cats?.items ?? []} />
      ) : (
        <Step3 f={f} set={setAndClear} onProbe={() => void runProbe()} sigStale={f.probeSig !== '' && f.probeSig !== configSig()} />
      )}
    </FormDialog>
  )
}

function StepBar({ step }: { step: number }) {
  return (
    <div className="flex items-center gap-2">
      {STEPS.map((label, i) => {
        const n = i + 1
        return (
          <div key={label} className="flex flex-1 items-center gap-2">
            <span
              className={
                'grid size-5 place-items-center rounded-full text-[11px] font-semibold ' +
                (n === step ? 'bg-foreground text-background' : n < step ? 'bg-success text-success-foreground' : 'bg-fill-tertiary text-muted-foreground')
              }
            >
              {n < step ? <Icon name="check" size={12} /> : n}
            </span>
            <span className={'text-[11.5px] ' + (n === step ? 'font-medium text-foreground' : 'text-muted-foreground')}>
              {label}
            </span>
            {n < STEPS.length ? <span className="h-px flex-1 bg-separator" /> : null}
          </div>
        )
      })}
    </div>
  )
}

/* ───────────────────────── Step 1：基础与传输 ───────────────────────── */

function Step1({
  f,
  set,
  errs,
  keyError,
}: {
  f: FormState
  set: <K extends keyof FormState>(k: K, v: FormState[K]) => void
  errs: Record<string, string | undefined>
  keyError: string
}) {
  const isRemote = f.transport !== 'stdio'
  const fieldErr = (k: string) =>
    errs[k] ? <p className="form-hint text-error-strong">{errs[k]}</p> : null
  return (
    <>
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
        <div>
          <Label>Name <span className="text-error-strong">*</span></Label>
          <Input value={f.name} onChange={(e) => set('name', e.target.value)} placeholder="例如：Web Search API" aria-invalid={Boolean(errs.name)} />
          {errs.name ? fieldErr('name') : <p className="form-hint">仅展示名，工具前缀取自 Key</p>}
        </div>
        <div>
          <Label>Key <span className="text-error-strong">*</span></Label>
          <Input
            value={f.key}
            onChange={(e) => set('key', e.target.value)}
            placeholder="例如：web-search"
            className="font-mono"
            aria-invalid={Boolean(keyError) || Boolean(errs.key)}
          />
          {keyError ? (
            <p className="form-hint text-error-strong">{keyError}</p>
          ) : errs.key ? (
            fieldErr('key')
          ) : (
            <p className="form-hint">工具前缀，唯一约束；重复返回 key_taken</p>
          )}
        </div>
      </div>

      <Row label="Enabled" hint="创建后立即进入聚合列表">
        <Switch checked={f.enabled} onCheckedChange={(v) => set('enabled', v)} />
      </Row>

      <div>
        <Label>Transport Type <span className="text-error-strong">*</span></Label>
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
            <Label>URL <span className="text-error-strong">*</span></Label>
            <Input value={f.url} onChange={(e) => set('url', e.target.value)} placeholder="https://api.example.com/mcp" className="font-mono" aria-invalid={Boolean(errs.url)} />
            {errs.url ? fieldErr('url') : null}
          </div>
          {f.transport === 'streamable_http' ? (
            <>
              <div>
                <Label>Protocol Version</Label>
                <Input value={f.protocolVersion} onChange={(e) => set('protocolVersion', e.target.value)} placeholder="2025-11-25" className="font-mono" />
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
            <Label>Command <span className="text-error-strong">*</span></Label>
            <Input value={f.command} onChange={(e) => set('command', e.target.value)} placeholder="npx" className="font-mono" aria-invalid={Boolean(errs.command)} />
            {errs.command ? fieldErr('command') : null}
          </div>
          <div>
            <Label>Args</Label>
            <Textarea rows={3} value={f.argsText} onChange={(e) => set('argsText', e.target.value)} placeholder={'-y\n@modelcontextprotocol/server-filesystem\n/tmp'} className="font-mono text-[12px]" />
            <p className="form-hint">每行一个参数，保存为 JSON 数组</p>
          </div>
          <div>
            <Label>Env</Label>
            <Textarea rows={2} value={f.envText} onChange={(e) => set('envText', e.target.value)} placeholder={'KEY=value'} className="font-mono text-[12px]" />
            <p className="form-hint">每行 KEY=value 或 KEY，保存为字符串数组</p>
          </div>
          <p className="flex items-start gap-2 rounded-lg border border-separator bg-fill-tertiary p-3 text-[12px] text-foreground">
            <Icon name="triangle-alert" size={15} className="mt-0.5 shrink-0 text-warning-strong" />
            stdio 凭据在子进程环境变量里：改密钥必须重建连接，且 stdio 不支持密钥池。
          </p>
        </>
      )}

      <div>
        <Label>Timeout</Label>
        <div className="relative max-w-70">
          <Input type="number" value={f.timeoutMs} onChange={(e) => set('timeoutMs', Number(e.target.value))} className="pr-12" aria-invalid={Boolean(errs.timeoutMs)} />
          <span className="absolute right-3 top-1/2 -translate-y-1/2 text-[12px] text-muted-foreground">ms</span>
        </div>
        {errs.timeoutMs ? fieldErr('timeoutMs') : <p className="form-hint">默认 30s。单个 server 卡死不影响其他 server</p>}
      </div>
    </>
  )
}

/* ───────────────────────── Step 2：鉴权与元信息 ───────────────────────── */

function Step2({
  f,
  set,
  errs,
  cats,
}: {
  f: FormState
  set: <K extends keyof FormState>(k: K, v: FormState[K]) => void
  errs: Record<string, string | undefined>
  cats: { id: string; name: string }[]
}) {
  const isRemote = f.transport !== 'stdio'
  const fieldErr = (k: string) =>
    errs[k] ? <p className="form-hint text-error-strong">{errs[k]}</p> : null
  const updateRow = (i: number, patch: Partial<{ k: string; v: string }>) =>
    set('headerRows', f.headerRows.map((r, idx) => (idx === i ? { ...r, ...patch } : r)))
  return (
    <>
      {isRemote ? (
        <>
          <Row label="是否需要 API Key" hint="直连下游（无需鉴权）时选「不需要」，凭据字段整体不出现">
            <div className="flex gap-1 rounded-lg border border-separator p-0.5">
              <button
                type="button"
                onClick={() => set('needsKey', true)}
                className={'rounded-md px-2.5 py-1 text-[12px] ' + (f.needsKey ? 'bg-foreground text-background' : 'text-muted-foreground')}
              >
                需要
              </button>
              <button
                type="button"
                onClick={() => set('needsKey', false)}
                className={'rounded-md px-2.5 py-1 text-[12px] ' + (!f.needsKey ? 'bg-foreground text-background' : 'text-muted-foreground')}
              >
                不需要
              </button>
            </div>
          </Row>
          {f.needsKey ? (
            <div className="space-y-2">
              <Label>Headers</Label>
              {f.headerRows.map((r, i) => (
                <div key={i} className="flex items-center gap-2">
                  <Input value={r.k} onChange={(e) => updateRow(i, { k: e.target.value })} placeholder="Authorization" className="flex-1 font-mono text-[12px]" />
                  <Input value={r.v} onChange={(e) => updateRow(i, { v: e.target.value })} placeholder="Bearer sk-…" className="flex-1 font-mono text-[12px]" />
                  <Button variant="ghost" size="icon-sm" aria-label="删除 Header" onClick={() => set('headerRows', f.headerRows.filter((_, idx) => idx !== i))}>
                    <Icon name="trash-2" size={14} />
                  </Button>
                </div>
              ))}
              <Button variant="outline" size="sm" onClick={() => set('headerRows', [...f.headerRows, { k: '', v: '' }])}>
                <Icon name="plus" size={13} /> 添加 Header
              </Button>
              {errs.headers ? fieldErr('headers') : null}
            </div>
          ) : (
            <p className="flex items-start gap-2 rounded-lg border border-separator bg-fill-tertiary p-3 text-[12px] text-foreground">
              <Icon name="info" size={15} className="mt-0.5 shrink-0 text-icon-muted" />
              直连 Server：网关不注入任何凭据，密钥池禁用，TPM 预估不适用。之后可随时切换。
            </p>
          )}
        </>
      ) : (
        <p className="flex items-start gap-2 rounded-lg border border-separator bg-fill-tertiary p-3 text-[12px] text-foreground">
          <Icon name="info" size={15} className="mt-0.5 shrink-0 text-icon-muted" />
          stdio 下游不支持密钥池，凭据随子进程环境变量传入，此处无需配置。
        </p>
      )}

      {isRemote && f.needsKey ? (
        <div>
          <Label>estTokensPerCall</Label>
          <div className="relative max-w-70">
            <Input type="number" value={f.estTokensPerCall} onChange={(e) => set('estTokensPerCall', e.target.value)} placeholder="例如 5000" className="pr-16" aria-invalid={Boolean(errs.estTokensPerCall)} />
            <span className="absolute right-3 top-1/2 -translate-y-1/2 text-[12px] text-muted-foreground">tokens</span>
          </div>
          {errs.estTokensPerCall ? fieldErr('estTokensPerCall') : <p className="form-hint">未计量 TPM 时用于三级回退估算</p>}
        </div>
      ) : null}

      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
        <div>
          <Label>
            分类 <span className="text-error-strong">*</span>
          </Label>
          {/* 分类数随 Server 增长，走带搜索的 Combobox */}
          <Combobox
            value={f.categoryId}
            onChange={(v) => set('categoryId', v)}
            options={cats.map((c) => ({ value: c.id, label: c.name }))}
            className="mt-1.5 w-full"
            placeholder="请选择分类"
            searchPlaceholder="搜索分类…"
            emptyText="还没有分类"
            aria-label="分类"
          />
          {errs.categoryId ? fieldErr('categoryId') : <p className="form-hint">分类决定工具名前缀段与检索组织，必选</p>}
        </div>
        <div>
          <Label>标签 (逗号分隔)</Label>
          <Input value={f.tagsText} onChange={(e) => set('tagsText', e.target.value)} placeholder="search, web" />
        </div>
      </div>
      <div>
        <Label>描述</Label>
        <Textarea rows={2} value={f.description} onChange={(e) => set('description', e.target.value)} placeholder="选填" />
      </div>
    </>
  )
}

/* ───────────────────── Step 3：探活与选择暴露的工具 ───────────────────── */

function Step3({
  f,
  set,
  onProbe,
  sigStale,
}: {
  f: FormState
  set: <K extends keyof FormState>(k: K, v: FormState[K]) => void
  onProbe: () => void
  sigStale: boolean
}) {
  const selectedCount = f.probeTools.filter((t) => f.probeSelected[t.name]).length
  const allSelected = f.probeTools.length > 0 && selectedCount === f.probeTools.length
  const toggleAll = (on: boolean) =>
    set('probeSelected', Object.fromEntries(f.probeTools.map((t) => [t.name, on])))

  return (
    <>
      {/* ── 探活区 ── */}
      {f.probeStatus === 'idle' || sigStale ? (
        <div className="rounded-lg border border-separator bg-fill-tertiary p-4">
          <p className="text-[12.5px] text-foreground">
            {sigStale
              ? '连接配置在上次探活后发生了变化，旧的工具清单已作废。'
              : '先探活验证连通性，探活成功后列出下游工具，由你勾选哪些对外暴露。'}
          </p>
          <Button variant="primary" size="sm" className="mt-3" onClick={onProbe}>
            <Icon name="radio" size={14} /> 开始探活
          </Button>
        </div>
      ) : f.probeStatus === 'probing' ? (
        <div className="flex items-center gap-2 rounded-lg border border-separator bg-fill-tertiary p-4 text-[12.5px] text-muted-foreground">
          <Icon name="refresh-cw" size={14} className="animate-spin" />
          正在探活（L1 可达性 → L2 协议协商 → L3 工具清单）…
        </div>
      ) : f.probeStatus === 'ok' ? (
        <div className="rounded-lg border border-separator p-4">
          <div className="flex flex-wrap items-center gap-2 text-[12px]">
            <Icon name="circle-check" size={15} className="text-success-strong" />
            <span className="font-medium text-foreground">探活成功</span>
            <span className="text-muted-foreground">
              共 {f.probeTools.length} 个工具 · 已选 {selectedCount} 个
              {selectedCount < f.probeTools.length ? '，未选的不会对外暴露' : ''}
            </span>
            <Button variant="ghost" size="sm" className="ml-auto" onClick={onProbe}>
              <Icon name="refresh-cw" size={13} /> 重新探活
            </Button>
          </div>

          {/* 工具勾选清单：名字来自探活的 tools/list，不再手抄模式串 */}
          <div className="mt-3 overflow-hidden rounded-lg border border-separator">
            <div className="flex items-center gap-3 border-b border-separator bg-fill-tertiary px-3 py-2">
              <Checkbox
                checked={allSelected ? true : selectedCount > 0 ? 'indeterminate' : false}
                onCheckedChange={(v) => toggleAll(v === true)}
                aria-label="全选 / 全不选"
              />
              <span className="text-[11px] font-semibold tracking-[0.06em] text-icon-muted uppercase">
                全选（{f.probeTools.length}）
              </span>
            </div>
            <div className="max-h-64 overflow-y-auto divide-y divide-separator">
              {f.probeTools.map((t) => (
                <label
                  key={t.name}
                  className="flex cursor-pointer items-start gap-3 px-3 py-2.5 transition-colors hover:bg-fill-secondary/60"
                >
                  <Checkbox
                    checked={Boolean(f.probeSelected[t.name])}
                    onCheckedChange={(v) =>
                      set('probeSelected', { ...f.probeSelected, [t.name]: v === true })
                    }
                    className="mt-0.5"
                  />
                  <div className="min-w-0 flex-1">
                    <div className="truncate font-mono text-[12.5px] text-foreground" title={t.name}>
                      {t.name}
                    </div>
                    {t.description ? (
                      <div className="mt-0.5 truncate text-[11.5px] text-muted-foreground" title={t.description}>
                        {t.description}
                      </div>
                    ) : null}
                  </div>
                  <code className="mt-0.5 shrink-0 font-mono text-[11px] text-icon-muted">
                    {f.key.trim() || 'key'}__{t.name}
                  </code>
                </label>
              ))}
            </div>
          </div>
        </div>
      ) : (
        <div className="rounded-lg border border-separator bg-error-surface/60 p-4">
          <div className="flex items-center gap-2 text-[12.5px]">
            <Icon name="x-circle" size={15} className="text-error-strong" />
            <span className="font-medium text-foreground">探活失败</span>
            {f.probeFail?.kind ? <code className="font-mono text-[11px] text-error-strong">{f.probeFail.kind}</code> : null}
          </div>
          {f.probeFail?.hint ? <p className="mt-1.5 text-[12px] text-foreground">{f.probeFail.hint}</p> : null}
          {f.probeFail?.detail ? (
            <p className="mt-1 truncate font-mono text-[11.5px] text-muted-foreground" title={f.probeFail.detail}>
              {f.probeFail.detail}
            </p>
          ) : null}
          {f.probeFail?.stderr ? (
            <pre className="mt-2 max-h-28 overflow-y-auto rounded-md bg-fill-tertiary p-2 font-mono text-[11px] text-muted-foreground">
              {f.probeFail.stderr}
            </pre>
          ) : null}
          <div className="mt-3 flex items-center gap-2">
            <Button variant="outline" size="sm" onClick={onProbe}>
              <Icon name="refresh-cw" size={13} /> 重新探活
            </Button>
          </div>
          <Row
            label="跳过筛选，暴露全部"
            hint="探不上的下游也可以先创建（记为 offline），之后在工具页再筛"
          >
            <Switch checked={f.fallbackAll} onCheckedChange={(v) => set('fallbackAll', v)} />
          </Row>
        </div>
      )}

      <Row label="创建后立即探活" hint="?probe=1 · 刷新网关的工具缓存，让勾选立即生效">
        <Switch checked={f.probe} onCheckedChange={(v) => set('probe', v)} />
      </Row>

      <div className="rounded-lg border border-separator bg-fill-tertiary p-3 text-[12px] text-muted-foreground">
        <p className="mb-1 font-medium text-foreground">将提交以下配置：</p>
        <p className="font-mono">key: {f.key || '—'}</p>
        <p className="font-mono">name: {f.name || '—'}</p>
        <p className="font-mono">transport: {f.transport}</p>
        <p className="font-mono">target: {f.transport === 'stdio' ? f.command || '—' : f.url || '—'}</p>
        <p className="font-mono">auth: {f.transport === 'stdio' ? 'stdio 环境变量' : f.needsKey ? 'header' : 'none'}</p>
        <p className="font-mono">
          filter:{' '}
          {f.probeStatus === 'ok'
            ? allSelected
              ? '全部暴露（不过滤）'
              : `allow · 暴露 ${selectedCount}/${f.probeTools.length} 个`
            : f.fallbackAll
              ? '全部暴露（探活失败兜底）'
              : '待探活后选择'}
        </p>
      </div>
    </>
  )
}

function Row({ label, hint, children }: { label: string; hint?: string; children: ReactNode }) {
  return (
    <div className="flex items-center justify-between gap-3">
      <div>
        <Label className="mb-0">{label}</Label>
        {hint ? <p className="form-hint mt-0">{hint}</p> : null}
      </div>
      {children}
    </div>
  )
}
