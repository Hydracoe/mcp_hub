import { useCallback, useEffect, useState } from 'react'

import { ApiError } from '@/lib/api/client'
import { probeServerDraft } from '@/lib/api/endpoints'
import { useCategories, useCreateServer } from '@/lib/api/queries'
import { toastError, toastSuccess } from '@/lib/toast'

import {
  EMPTY_SERVER_FORM,
  clearedProbe,
  connectionSignature,
  countSelectedTools,
  isProbeStale,
  toCreateRequest,
  toProbeRequest,
  type ServerFormState,
  type WizardStep,
} from './form-model'
import { validateAll, validateAuth, validateBasics, type FieldErrors } from './validation'

interface UseServerWizardOptions {
  open: boolean
  onOpenChange: (open: boolean) => void
}

/** 新增 Server 向导的状态机：分步校验、探活与失效判定、提交都在这里闭环。
 *  表单用一个大对象 `f` 而非 useReducer。 */
export function useServerWizard({ open, onOpenChange }: UseServerWizardOptions) {
  const [step, setStep] = useState<WizardStep>(1)
  const [f, setF] = useState<ServerFormState>(EMPTY_SERVER_FORM)
  /** `key_taken` 的行内错误，独立于字段校验。 */
  const [keyError, setKeyError] = useState('')
  const [errs, setErrs] = useState<FieldErrors>({})

  const create = useCreateServer()
  const { data: cats } = useCategories()

  // 每次打开都从空表单起步：留着上一次的输入会让人以为改动已经生效。
  useEffect(() => {
    if (!open) return
    setStep(1)
    setF(EMPTY_SERVER_FORM)
    setKeyError('')
    setErrs({})
  }, [open])

  const set = useCallback(
    <K extends keyof ServerFormState>(k: K, v: ServerFormState[K]) =>
      setF((p) => ({ ...p, [k]: v })),
    [],
  )

  /** 字段改动即清除该字段的校验错误。 */
  const setAndClear = useCallback(
    <K extends keyof ServerFormState>(k: K, v: ServerFormState[K]) => {
      setErrs((p) => (p[k as string] ? { ...p, [k as string]: undefined } : p))
      set(k, v)
    },
    [set],
  )

  const sigStale = isProbeStale(f)

  const goBack = useCallback(() => setStep((s) => (s > 1 ? ((s - 1) as WizardStep) : s)), [])

  const goNext = useCallback(() => {
    if (step === 1) {
      const e = validateBasics(f)
      setErrs(e)
      if (Object.keys(e).length) return
    }
    if (step === 2) {
      const e = validateAuth(f)
      setErrs(e)
      if (Object.keys(e).length) return
      // 进入探活步前：连接配置变过就把旧探活结果作废，避免拿陈旧清单勾选。
      if (f.probeSig && f.probeSig !== connectionSignature(f)) {
        setF((p) => ({ ...p, ...clearedProbe() }))
      }
    }
    setStep((s) => (s < 3 ? ((s + 1) as WizardStep) : s))
  }, [f, step])

  const runProbe = useCallback(async () => {
    const { errors, first } = validateAll(f)
    setErrs(errors)
    if (first) return toastError(new Error(first))

    setKeyError('')
    set('probeStatus', 'probing')
    const sig = connectionSignature(f)

    try {
      const res = await probeServerDraft(toProbeRequest(f))

      if (res.ok && (res.tools?.length ?? 0) > 0) {
        setF((p) => ({
          ...p,
          probeStatus: 'ok',
          probeTools: res.tools ?? [],
          probeSelected: Object.fromEntries((res.tools ?? []).map((t) => [t.name, true])),
          probeFail: null,
          probeSig: sig,
        }))
        return
      }

      // 协商成功但下游一个工具都没有：按失败处理，勾选无从谈起。
      const fail = res.ok
        ? { kind: 'no_tools', detail: '下游没有声明任何工具', hint: '确认这是工具型 server 而不是资源型' }
        : { kind: res.kind, hint: res.hint, detail: res.detail, stderr: res.stderr }

      setF((p) => ({ ...p, probeStatus: 'fail', probeFail: fail, probeSig: sig }))
    } catch (e) {
      setF((p) => ({
        ...p,
        probeStatus: 'fail',
        probeFail: { kind: 'request_failed', detail: e instanceof Error ? e.message : String(e) },
      }))
    }
  }, [f, set])

  const submit = useCallback(async () => {
    const { errors, first } = validateAll(f)
    setErrs(errors)
    if (first) return toastError(new Error(first))

    // 探活已成功但配置后来又改了：旧清单对应的可能已经不是这套连接。
    if (isProbeStale(f)) return toastError(new Error('连接配置已变化，请重新探活后再创建'))

    if (f.probeStatus === 'ok' && !f.fallbackAll && countSelectedTools(f) === 0) {
      return toastError(new Error('至少勾选一个要启用的工具'))
    }

    setKeyError('')
    try {
      await create.mutateAsync({ body: toCreateRequest(f), probe: f.probe })
      toastSuccess('Server 已创建', f.probe ? '已带探活结果' : undefined)
      onOpenChange(false)
    } catch (e) {
      // key_taken 回到 key 字段做行内错误，而不是只弹 toast
      if (e instanceof ApiError && e.code === 'key_taken') {
        setKeyError(e.message)
        setStep(1)
        return
      }
      toastError(e, '创建失败')
    }
  }, [create, f, onOpenChange])

  return {
    step,
    form: f,
    /** 字段级错误。 */
    errs,
    /** key 重复的行内错误。 */
    keyError,
    /** 探活成功后连接配置又被改过。 */
    sigStale,
    /** 分类下拉的选项。 */
    categories: cats?.items ?? [],
    pending: create.isPending,
    set: setAndClear,
    goNext,
    goBack,
    runProbe: () => void runProbe(),
    submit: () => void submit(),
  }
}
