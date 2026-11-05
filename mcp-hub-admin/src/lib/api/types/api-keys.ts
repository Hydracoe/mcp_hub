/** 一把接入密钥：上游 AI 客户端调 `/mcp` 时出示的凭据，由网关签发。
 *  与「密钥池」区分：密钥池是网关调下游 server 的凭据（按 Server 隔离）。
 *  字段与后端 apikeyItem 对应，刻意不含调用次数。 */
export interface ApiKeyView {
  id: string
  /** 标识调用方，例如「Claude Desktop · 我的 Mac」。 */
  name: string
  /** 明文前 10 位（含 `mh-` 前缀）；明文本身不回显。 */
  prefix: string
  enabled: boolean
  /** Unix 毫秒；0 表示从未被使用过。 */
  lastUsedAt: number
  createdAt: number
}

/** 创建响应：明文 `key` 只出现这一次，库里只存 sha256。丢了只能删掉重建。 */
export interface ApiKeyCreateResponse extends ApiKeyView {
  key: string
}
