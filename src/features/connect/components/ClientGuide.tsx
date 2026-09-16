import { Icon } from '@/components/common/Icon'
import { CodeChip } from '@/components/common/KeyValue'

interface Props {
  mcpEndpoint: string
}

interface ClientCard {
  icon: 'terminal' | 'mouse-pointer-click' | 'code'
  name: string
  steps: string[]
  note: string
}

export function ClientGuide({ mcpEndpoint }: Props) {
  const sse = mcpEndpoint.replace(/\/mcp$/, '/sse') || `${mcpEndpoint}/sse`

  const cards: ClientCard[] = [
    {
      icon: 'terminal',
      name: 'Claude Code',
      steps: [
        '编辑配置文件 ~/.claude/config.json',
        '在 mcpServers 中加入下面的 JSON',
        '重启 Claude Code 使配置生效',
      ],
      note: '工具名带 serverKey__ 前缀标识来源，如 web-search__search。',
    },
    {
      icon: 'mouse-pointer-click',
      name: 'Cursor',
      steps: ['打开 Settings → MCP', '添加新的 MCP Server', `类型选 URL，填入 ${mcpEndpoint}`],
      note: '网关使用 Streamable HTTP 协议，Cursor 直接填 URL 即可。',
    },
    {
      icon: 'code',
      name: 'Codex / 其他',
      steps: [
        '在客户端的 MCP 配置中添加端点',
        `使用 ${mcpEndpoint} 作为 URL`,
        `或改用 SSE：${sse}`,
      ],
      note: '某下游不可用时，其工具仍在列表，但调用会返回带原因的失败结果。',
    },
  ]

  return (
    <div className="client-grid">
      {cards.map((c) => (
        <article key={c.name} className="ui-panel flex flex-col p-5">
          <div className="mb-4 flex items-center gap-2">
            <Icon name={c.icon} size={18} className="text-brand-strong" />
            <span className="text-sm font-semibold text-foreground">{c.name}</span>
            <Icon name="external-link" size={14} className="ml-auto text-icon-muted" />
          </div>
          <ol className="flex-1 space-y-2.5 text-sm text-foreground">
            {c.steps.map((s, i) => (
              <li key={i} className="flex gap-2">
                <span className="shrink-0 font-semibold text-brand-strong">{i + 1}.</span>
                <span>
                  {s.includes(mcpEndpoint) || s.includes(sse) ? <CodeChip>{s}</CodeChip> : s}
                </span>
              </li>
            ))}
          </ol>
          <div className="mt-4 flex items-start gap-1.5 border-t border-separator pt-3 text-xs text-muted-foreground">
            <Icon name="circle-info" size={14} className="mt-0.5 shrink-0" />
            <span>{c.note}</span>
          </div>
        </article>
      ))}
    </div>
  )
}
