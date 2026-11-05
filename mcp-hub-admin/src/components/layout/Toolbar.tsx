import { useNavigate } from 'react-router-dom'

import { Icon } from '@/components/common/Icon'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { useBackendConnection, useOverview } from '@/lib/api/queries'
import { cn } from '@/lib/utils'

export interface ToolbarProps {
  title: string
  backTo?: string
  /** 版本 chip，来自 `/api/overview`，不要写死。 */
  version?: string
  className?: string
}

/** 徽标三态：连接状态决定颜色与文案，网关是否有工具只在 tooltip 里补一句。 */
const CONNECTION_VIEW = {
  connected: { dot: 'ok', variant: 'success', label: 'Ready' },
  connecting: { dot: 'idle', variant: 'neutral', label: 'Connecting' },
  disconnected: { dot: 'error', variant: 'error', label: 'Not Ready' },
} as const

/** 顶部工具栏（52px 材质栏）：只放跨页面稳定元素（返回/标题/版本/就绪）。
 *  页面按钮走 `PageLayout.actions`，否则导航栏会变成第二排工具栏。 */
export function Toolbar({ title, backTo, version, className }: ToolbarProps) {
  const navigate = useNavigate()

  /* 徽标说的是"前端连上后端了吗"，不是"网关有工具可提供吗"：
     后者在空库（还没接下游）时是 false，会天天误报成故障。 */
  const connection = useBackendConnection()
  const { data } = useOverview()
  const view = CONNECTION_VIEW[connection]

  const hint =
    connection === 'disconnected'
      ? '无法连接后端：请确认 mcp-hub 服务已启动、端口与代理配置正确'
      : connection === 'connecting'
        ? '正在连接后端…'
        : data?.ready
          ? '后端已连接，网关就绪（有工具可提供给 AI 客户端）'
          : '后端已连接；当前没有可用工具——接入下游 Server 并通过探活后即可用'

  return (
    <header className={cn('app-toolbar', className)}>
      {backTo ? (
        <Button
          size="icon"
          variant="ghost"
          aria-label="返回"
          onClick={() => navigate(backTo)}
          className="shrink-0"
        >
          <Icon name="chevron-left" size={16} />
        </Button>
      ) : null}
      <h1 className="toolbar-title">{title}</h1>

      <div className="toolbar-trail">
        {/* 后端可能不返回版本号：此时不渲染 chip——兜底的「—」
            会被当成一条莫名其妙的横线。 */}
        {version ? <span className="chip chip-quiet">{version}</span> : null}
        <Tooltip>
          <TooltipTrigger asChild>
            <Badge
              variant={view.variant}
              className="rounded-full! h-5.5! px-2.5! gap-1.5! cursor-default"
            >
              <span className="status-dot" data-state={view.dot} />
              {view.label}
            </Badge>
          </TooltipTrigger>
          <TooltipContent>{hint}</TooltipContent>
        </Tooltip>
      </div>
    </header>
  )
}
