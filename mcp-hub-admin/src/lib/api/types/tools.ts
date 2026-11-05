export interface ToolEntry {
  exposedName: string
  name: string
  serverId: string
  serverKey: string
  description?: string
  inputSchema?: Record<string, unknown>
  /** 下游不可用时为 false —— 工具仍在列表里，调用会返回带原因的失败。 */
  available: boolean

  /** 人工开关的当前值；关着 = 不进聚合列表，对 AI 不可见。 */
  enabled?: boolean
  /** 探活新发现、默认禁用、且从未被人处理过：等一个「启用或拒绝」的决定。 */
  pending?: boolean

  callCount?: number
  successRate?: number
  category?: string
}
