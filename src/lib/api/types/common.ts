/** 跨两个及以上领域共用的契约类型与常量；只一个领域用的放它自己的文件。 */

export type Transport = 'stdio' | 'sse' | 'streamable_http'

export type ServerStatus = 'healthy' | 'degraded' | 'offline' | 'unknown'

/** 生效的鉴权形态。`none` 是「这个下游天生不需要密钥」，不等于「还没配」。 */
export type AuthMode = 'none' | 'header'

export type CredentialState = 'active' | 'cooling' | 'disabled'

export type DisabledKind = 'INVALID_KEY' | 'KEY_REVOKED' | 'QUOTA_EXHAUSTED'

/** 下游失败分类，用于日志筛选与诊断文案。 */
export const ERROR_KINDS = [
  'DNS_FAIL',
  'CONN_REFUSED',
  'TLS_FAIL',
  'TIMEOUT',
  'AUTH_REQUIRED',
  'NOT_FOUND',
  'UPSTREAM_ERROR',
  'RATE_LIMITED',
  'QUOTA_EXHAUSTED',
  'NOT_MCP',
  'PROTOCOL_MISMATCH',
  'CAPABILITY_MISSING',
  'SPAWN_FAILED',
  'EXIT_EARLY',
  'HANDSHAKE_TIMEOUT',
  'SERVER_DRAINING',
  'CANCELED',
  'NO_USABLE_KEY',
  'UNKNOWN',
] as const
export type ErrorKind = (typeof ERROR_KINDS)[number] | ''

/** 统一错误体：`{"code": "...", "error": "..."}`。 */
export type ApiErrorCode =
  | 'not_found'
  | 'key_taken'
  /** 分类中文名已被占用 */
  | 'category_taken'
  | 'invalid_request'
  | 'unauthorized'
  | 'unavailable'
  | 'internal'
  /** 密钥池不支持（stdio 下游） */
  | 'key_pool_unsupported'
  | 'import_failed'
  /** 同一 server 下已存在同 label 的密钥 */
  | 'credential_exists'
  /** 配了密钥池但没有可用密钥（全部冷却 / 停用） */
  | 'no_usable_key'
