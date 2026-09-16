import { CopyButton } from '@/components/common/CopyButton'
import type { ConnectResponse } from '@/lib/api/types'

interface Props {
  connect: ConnectResponse
}

interface EndpointDef {
  method: 'POST' | 'GET'
  path: string
  title: string
  desc: string
  /** 缺失时（如未启用的 /sse）标注「未启用」而非渲染空卡。 */
  disabled?: boolean
}

function MethodBadge({ method }: { method: 'POST' | 'GET' }) {
  const cls =
    method === 'POST'
      ? 'bg-primary text-primary-foreground'
      : 'bg-success text-success-foreground'
  return (
    <span
      className={`inline-flex items-center rounded-full px-2 py-0.5 font-mono text-xs font-bold ${cls}`}
    >
      {method}
    </span>
  )
}

export function EndpointGrid({ connect }: Props) {
  const readyz = connect.healthEndpoint.replace(/\/healthz$/, '/readyz') || `${connect.healthEndpoint}/readyz`

  const endpoints: EndpointDef[] = [
    {
      method: 'POST',
      path: connect.mcpEndpoint,
      title: 'MCP 主端点 (Streamable HTTP)',
      desc: '支持 Initialize、Tools/List、Tools/Call 等标准 MCP 方法。',
    },
    {
      method: 'GET',
      path: connect.sseEndpoint ?? '',
      title: 'SSE 端点',
      desc: 'Server-Sent Events 流式连接，兼容旧版客户端。',
      disabled: !connect.sseEndpoint,
    },
    {
      method: 'GET',
      path: connect.healthEndpoint,
      title: '存活探针',
      desc: '返回 200 表示进程存活。',
    },
    {
      method: 'GET',
      path: readyz,
      title: '就绪探针',
      desc: '返回 200 表示至少一个 Server 就绪。',
    },
  ]

  return (
    <div className="endpoint-grid">
      {endpoints.map((ep) => (
        <article key={ep.path + ep.title} className="ui-panel p-5">
          <div className="flex items-start justify-between gap-2">
            <div className="flex min-w-0 items-center gap-2">
              <MethodBadge method={ep.method} />
              {ep.disabled ? (
                <span className="inline-flex items-center gap-1.5 rounded-full border border-separator px-2 py-0.5 text-xs text-muted-foreground">
                  <span className="size-1.5 rounded-full bg-fill-tertiary" />
                  未启用
                </span>
              ) : (
                <span className="truncate font-mono text-sm font-semibold text-foreground">
                  {ep.path}
                </span>
              )}
            </div>
            {!ep.disabled ? (
              <CopyButton value={ep.path} label="复制端点路径" />
            ) : null}
          </div>
          <p className="mt-2 text-sm font-medium text-foreground">{ep.title}</p>
          <p className="mt-1 text-sm text-muted-foreground">{ep.desc}</p>
        </article>
      ))}
    </div>
  )
}
