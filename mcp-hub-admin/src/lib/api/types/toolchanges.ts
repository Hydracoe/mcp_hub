export type ToolChangeAction =
  | 'added'
  | 'removed'
  | 'updated'
  | 'enabled'
  | 'disabled'

export interface ToolChangeItem {
  id: string
  serverId: string
  serverKey: string
  toolName: string
  action: ToolChangeAction
  detail?: string
  /** system = 探活对账自动写入；admin = 管理接口人工操作。 */
  actor: 'system' | 'admin' | string
  /** epoch 毫秒。 */
  ts: number
}

export interface ToolChangeListResponse {
  total: number
  limit: number
  offset: number
  items: ToolChangeItem[]
}

export interface ToolChangeQuery {
  serverId?: string
  limit?: number
  offset?: number
}
