import { useState } from 'react'
import { Link } from 'react-router-dom'
import {
  Dialog,
  DialogBody,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Icon } from '@/components/common/Icon'
import { CodeBlock } from '@/components/common/CodeBlock'
import { ServerStatusBadge, TransportTag } from '@/components/common/StatusBadge'
import { useTools } from '@/lib/api/queries'
import { formatInt, formatPercent, shortTarget } from '@/lib/format'
import type { ServerSummary, ToolEntry } from '@/lib/api/types'

interface Props {
  server: ServerSummary | null
  open: boolean
  onOpenChange: (open: boolean) => void
}

/**
 * 工具管理弹框：只读列表（缺口 D-只读，写操作在 /tools 独立路由与密钥池页）。
 * ⚠ 设计稿原版把弹窗常驻 DOM（display:none / drawer-hidden），这里改成 Radix 受控 open。
 */
export function ServerToolsDialog({ server, open, onOpenChange }: Props) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent size="xl">
        {server ? <ToolsBody server={server} /> : null}
      </DialogContent>
    </Dialog>
  )
}

function ToolsBody({ server }: { server: ServerSummary }) {
  const { data, isLoading } = useTools({ serverKey: server.key })
  const items = data?.items ?? []
  const total = data?.total ?? 0

  return (
    <>
      <DialogHeader>
        <DialogTitle className="flex items-center gap-2">
          <Icon name="wrench" size={16} className="text-icon-muted" />
          工具管理
        </DialogTitle>
        <DialogDescription className="font-mono">
          {server.key} · GET /api/servers/{server.key}/tools
        </DialogDescription>
      </DialogHeader>

      <DialogBody>
        <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5 text-[11.5px] text-muted-foreground">
          <TransportTag transport={server.transport} />
          <ServerStatusBadge status={server.status} />
          <span className="font-mono">{shortTarget(server.target)}</span>
          <span>
            工具 <b className="text-foreground tabular-nums">{server.enabledToolCount ?? total} / {total}</b> 启用
          </span>
          {server.toolFilterMode !== 'allow_all' ? (
            <Badge variant="warning">过滤中 · {server.toolFilterMode}</Badge>
          ) : null}
        </div>

        {isLoading ? (
          <p className="py-8 text-center text-[12px] text-muted-foreground">加载工具列表…</p>
        ) : items.length === 0 ? (
          <p className="py-8 text-center text-[12px] text-muted-foreground">该 Server 暂无已声明工具</p>
        ) : (
          <ul className="divide-y divide-separator rounded-lg border border-separator">
            {items.map((t) => (
              <ToolRow key={t.exposedName} tool={t} />
            ))}
          </ul>
        )}
      </DialogBody>

      <DialogFooter>
        <span className="text-[11.5px] text-muted-foreground">只读视图 · 启停与新增请到工具管理页</span>
        <Button variant="outline" size="sm" asChild>
          <Link to={`/tools?server=${server.id}`}>
            <Icon name="external-link" size={13} />
            在工具管理中查看
          </Link>
        </Button>
      </DialogFooter>
    </>
  )
}

function ToolRow({ tool }: { tool: ToolEntry }) {
  const [open, setOpen] = useState(false)
  const hasSchema = tool.inputSchema && Object.keys(tool.inputSchema).length > 0
  return (
    <li className="px-3 py-2.5">
      <div className="flex items-start gap-2">
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2">
            <span className="truncate font-mono text-[12px] text-foreground" title={tool.exposedName}>
              {tool.exposedName}
            </span>
            {tool.name !== tool.exposedName ? (
              <span className="shrink-0 font-mono text-[11px] text-muted-foreground" title={tool.name}>
                ← {tool.name}
              </span>
            ) : null}
          </div>
          {tool.description ? (
            <p className="mt-0.5 truncate text-[11.5px] text-muted-foreground">{tool.description}</p>
          ) : null}
        </div>
        <div className="flex shrink-0 items-center gap-1.5">
          {tool.available ? (
            <Badge variant="success">可用</Badge>
          ) : (
            <Badge variant="error">不可用</Badge>
          )}
          {tool.enabled ? <Badge variant="neutral">已启用</Badge> : <Badge variant="outline">已停用</Badge>}
          {typeof tool.successRate === 'number' ? (
            <span className="font-mono text-[11px] text-muted-foreground tabular-nums">
              {formatPercent(tool.successRate)}
            </span>
          ) : null}
          {typeof tool.callCount === 'number' ? (
            <span className="font-mono text-[11px] text-muted-foreground tabular-nums">
              {formatInt(tool.callCount)} 次
            </span>
          ) : null}
          {hasSchema ? (
            <Button variant="ghost" size="icon-sm" aria-label="查看入参 Schema" onClick={() => setOpen((v) => !v)}>
              <Icon name={open ? 'chevron-up' : 'chevron-down'} size={14} />
            </Button>
          ) : null}
        </div>
      </div>
      {open && hasSchema ? (
        <div className="mt-2">
          <CodeBlock code={JSON.stringify(tool.inputSchema, null, 2)} language="json" maxHeight={240} />
        </div>
      ) : null}
    </li>
  )
}
