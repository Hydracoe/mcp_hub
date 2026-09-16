/** GET /api/connect：接入指引页需要的端点清单与一键复制配置。 */
export interface ConnectResponse {
  mcpEndpoint: string
  sseEndpoint?: string
  healthEndpoint: string
  /** 一键复制的客户端配置：{"mcp-hub": "<url>"}。 */
  mcpServers: Record<string, string>
  toolCount: number
  serverCount: number
  summary: string
  note: string
}
