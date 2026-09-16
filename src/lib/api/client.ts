import type { ApiErrorCode } from './types'

/** 统一错误体 `{code, error}` 的前端表示。界面按 `code` 分支（unauthorized/not_found/key_taken…），别按 message 文案匹配。 */
export class ApiError extends Error {
  readonly status: number
  readonly code: ApiErrorCode | string

  constructor(status: number, code: string, message: string) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
  }

  get isUnauthorized() {
    return this.status === 401 || this.code === 'unauthorized'
  }
}

const API_KEY_STORAGE = 'mcp-hub.admin.apiKey'

/** 管理接口鉴权令牌（`--api-key` 启用时必需）。仅存本机 localStorage。 */
export const apiKeyStorage = {
  read(): string {
    try {
      return localStorage.getItem(API_KEY_STORAGE) ?? ''
    } catch {
      return ''
    }
  },
  write(value: string) {
    try {
      if (value) localStorage.setItem(API_KEY_STORAGE, value)
      else localStorage.removeItem(API_KEY_STORAGE)
    } catch {
      /* 隐私模式下 localStorage 不可写，忽略即可 */
    }
  },
}

/** 401 的全局广播：AuthGate 订阅它来弹出解锁框。 */
type UnauthorizedListener = () => void
const unauthorizedListeners = new Set<UnauthorizedListener>()

export function onUnauthorized(fn: UnauthorizedListener) {
  unauthorizedListeners.add(fn)
  return () => unauthorizedListeners.delete(fn)
}

export interface RequestOptions {
  method?: 'GET' | 'POST' | 'PUT' | 'DELETE'
  /** 查询参数。`undefined` / 空串会被丢弃。 */
  query?: Record<string, string | number | boolean | undefined | null>
  body?: unknown
  signal?: AbortSignal
}

function buildUrl(path: string, query?: RequestOptions['query']) {
  if (!query) return path
  const sp = new URLSearchParams()
  for (const [k, v] of Object.entries(query)) {
    if (v === undefined || v === null || v === '') continue
    sp.set(k, String(v))
  }
  const qs = sp.toString()
  return qs ? `${path}?${qs}` : path
}

/** 发一次管理接口请求，统一走 fetch 封装（鉴权头、JSON、错误体归一）。 */
export async function request<T>(path: string, options: RequestOptions = {}): Promise<T> {
  const { method = 'GET', query, body, signal } = options
  const headers: Record<string, string> = { Accept: 'application/json' }

  const key = apiKeyStorage.read()
  if (key) headers['X-API-Key'] = key
  if (body !== undefined) headers['Content-Type'] = 'application/json'

  let res: Response
  try {
    res = await fetch(buildUrl(path, query), {
      method,
      headers,
      body: body === undefined ? undefined : JSON.stringify(body),
      signal,
    })
  } catch (err) {
    if (err instanceof DOMException && err.name === 'AbortError') throw err
    throw new ApiError(0, 'network', '无法连接到网关，请确认 mcp-hub 进程正在运行')
  }

  if (res.status === 204) return undefined as T

  const text = await res.text()
  let payload: unknown = undefined
  if (text) {
    try {
      payload = JSON.parse(text)
    } catch {
      payload = undefined
    }
  }

  if (!res.ok) {
    const body = (payload ?? {}) as { code?: string; error?: string }
    const err = new ApiError(
      res.status,
      body.code ?? httpFallbackCode(res.status),
      body.error || `请求失败（HTTP ${res.status}）`,
    )
    if (err.isUnauthorized) unauthorizedListeners.forEach((fn) => fn())
    throw err
  }

  return payload as T
}

function httpFallbackCode(status: number) {
  if (status === 401) return 'unauthorized'
  if (status === 404) return 'not_found'
  if (status === 409) return 'key_taken'
  if (status >= 500) return 'internal'
  return 'invalid_request'
}
