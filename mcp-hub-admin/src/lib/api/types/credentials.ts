import type { CredentialState, DisabledKind } from './common'

export interface CredentialView {
  id: string
  label: string
  /** 配了哪些头（键名）。 */
  headerKeys?: string[]
  /** 值末 4 位，用来区分是哪一把。 */
  fingerprint?: string
  configured: boolean

  enabled: boolean
  state: CredentialState

  disabledKind?: DisabledKind
  disabledAt?: number
  disabledNote?: string

  coolingUntil?: number
  coolingForMs?: number
  consecutiveLimited?: number

  rpmLimit: number

  rpmUsed: number
  windowEnd?: number

  picks: number
  lastUsedAt?: number
  lastError?: string

  callCount?: number
  failCount?: number
  successRate?: number
  p95Ms?: number

  createdAt: number
  updatedAt: number
}

export interface CredentialListResponse {
  total: number
  items: CredentialView[]
  /** false 表示退回单密钥模式。 */
  keyPool: boolean
}

export interface CredentialRequest {
  label: string
  headers?: Record<string, string>
  /** 下面三个二选一：直接给 headers，或给 value + 展开方式。 */
  value?: string
  authScheme?: 'bearer' | 'x-api-key' | 'header'
  headerName?: string
  rpmLimit?: number
}

export interface CredentialImportRequest {
  authScheme?: 'bearer' | 'x-api-key' | 'header'
  headerName?: string
  /** 一行一把，"名称=值" 或直接给值。 */
  lines: string[]
  rpmLimit?: number
}
