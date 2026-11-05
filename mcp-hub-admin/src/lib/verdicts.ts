import type { DisabledKind } from '@/lib/api/types'

/** 弃用原因中文文案，与后端 KeyVerdict.Human() 同义；原始枚举保留在 title 里，便于与日志对账。 */
const DISABLED_KIND_LABEL: Record<DisabledKind, string> = {
  INVALID_KEY: '凭据无效',
  KEY_REVOKED: '已吊销/封禁',
  QUOTA_EXHAUSTED: '额度耗尽',
}

/** 弃用原因的展示文案。未知值原样返回（后端加新判决时前端不至于显示空白）。 */
export function disabledKindLabel(kind?: string): string {
  if (!kind) return ''
  return DISABLED_KIND_LABEL[kind as DisabledKind] ?? kind
}
