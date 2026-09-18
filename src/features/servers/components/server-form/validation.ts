import type { ServerFormState } from './form-model'

/** 向导的字段校验。分步执行，错误挂在字段下面。
 *  规则一共七条，没引校验库——抽象比规则本身还多。 */

/** 字段名 → 错误文案。没有错误就不出现在对象里。 */
export type FieldErrors = Record<string, string | undefined>

/** 工具前缀：小写字母/数字开头，只含小写字母、数字、`-`、`_`。 */
export const SERVER_KEY_RE = /^[a-z0-9][a-z0-9_-]*$/

const HTTP_RE = /^https?:$/

const isPositiveInt = (n: number) => Number.isInteger(n) && n > 0

export function validateBasics(f: ServerFormState): FieldErrors {
  const e: FieldErrors = {}

  if (!f.name.trim()) e.name = '必填'

  if (!f.key.trim()) e.key = '必填'
  else if (!SERVER_KEY_RE.test(f.key.trim())) e.key = '仅允许小写字母、数字、-、_，且以字母/数字开头'

  if (!isPositiveInt(f.timeoutMs)) e.timeoutMs = '必须是正整数（毫秒）'

  if (f.transport === 'stdio') {
    if (!f.command.trim()) e.command = 'stdio 传输必须填写 Command'
    return e
  }

  if (!f.url.trim()) {
    e.url = '远程传输必须填写 URL'
    return e
  }
  try {
    const u = new URL(f.url.trim())
    if (!HTTP_RE.test(u.protocol)) throw new Error()
  } catch {
    e.url = '必须是合法的 http(s) 地址'
  }
  return e
}

export function validateAuth(f: ServerFormState): FieldErrors {
  const e: FieldErrors = {}

  if (!f.categoryId) e.categoryId = '必选：分类决定工具名前缀段'

  if (f.transport !== 'stdio' && f.needsKey) {
    const hasEmptyName = f.headerRows.some((r) => !r.k.trim() && r.v.trim())
    const hasAnyName = f.headerRows.some((r) => r.k.trim())
    if (!hasAnyName) e.headers = '至少填写一行 Header（名称必填）'
    else if (hasEmptyName) e.headers = '有 Header 只填了值没填名称，请补全或删除'
  }

  return e
}

/** 全量校验，返回合并后的错误表与「第一条错误文案」。 */
export function validateAll(f: ServerFormState): { errors: FieldErrors; first?: string } {
  const errors = { ...validateBasics(f), ...validateAuth(f) }
  return { errors, first: Object.values(errors)[0] }
}
