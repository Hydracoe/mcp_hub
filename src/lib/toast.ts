import { toast } from 'sonner'

import { ApiError } from './api/client'

/** 把失败统一呈现成 toast。后端错误体 `{code, error}` 里 `error` 已是写给用户的中文句子，直接用即可，别再包一层「操作失败」盖掉它。 */
export function toastError(error: unknown, fallback = '操作失败') {
  if (error instanceof ApiError) {
    const title = codeTitle(error.code)
    toast.error(title, { description: error.message, duration: 6000 })
    return
  }
  if (error instanceof Error) {
    toast.error(fallback, { description: error.message, duration: 6000 })
    return
  }
  toast.error(fallback)
}

/** 按错误码给一个比「错误」更有信息量的标题。 */
function codeTitle(code: string): string {
  switch (code) {
    case 'unauthorized':
      return '需要管理凭据'
    case 'not_found':
      return '对象不存在'
    case 'key_taken':
      return '命名冲突'
    case 'credential_exists':
    case 'credential_duplicate':
      return '密钥标签已被占用'
    case 'category_taken':
      return '中文名已被占用'
    case 'invalid_request':
      return '请求不合法'
    case 'key_pool_unsupported':
      return '该下游不支持密钥池'
    case 'unavailable':
      return '服务暂不可用'
    case 'network':
      return '连不上网关'
    case 'internal':
      return '网关内部错误'
    default:
      return '操作失败'
  }
}

export function toastSuccess(title: string, description?: string) {
  toast.success(title, { description })
}

export { toast }
