import { useNavigate } from 'react-router-dom'

import { Icon } from '@/components/common/Icon'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { useUiStore } from '@/stores/ui-store'
import { cn } from '@/lib/utils'

export interface ToolbarProps {
  title: string
  /** 传了就渲染返回按钮。 */
  backTo?: string
  /** 版本 chip。来自 `/api/overview`，不要写死 —— 设计稿里各页写死的版本号互相打架。 */
  version?: string
  /** 网关就绪状态。 */
  ready?: boolean
  className?: string
}

/**
 * 顶部工具栏（52px 材质栏）。
 *
 * 这里**只放跨页面稳定的东西**：返回、标题、主题开关、版本 chip、就绪徽标。
 * 页面自己的功能按钮一律走 `PageLayout` 的 `actions`，渲染在内容区 ——
 * 导航栏是全局 chrome，跟着侧边栏选中项变来变去会让它变成第二排页面工具栏，
 * 也和设计稿不符（设计稿的 `.toolbar-trail` 里只有两枚 chip）。
 *
 * 版本号与就绪状态是每个页面都要的，所以收在工具栏里统一渲染；
 * 设计稿把这两枚 chip 复制到了 10 个文件里，于是出现了
 * server-detail 的 `v2.1.0` 与其余页面的 `v1.0.0` 并存。
 */
export function Toolbar({ title, backTo, version, ready, className }: ToolbarProps) {
  const navigate = useNavigate()
  const theme = useUiStore((s) => s.theme)
  const setTheme = useUiStore((s) => s.setTheme)

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
        <Tooltip>
          <TooltipTrigger asChild>
            <Button
              size="icon"
              variant="ghost"
              aria-label="切换深色模式"
              onClick={() => setTheme(theme === 'dark' ? 'light' : 'dark')}
            >
              <Icon name={theme === 'dark' ? 'lightbulb' : 'house'} size={15} />
            </Button>
          </TooltipTrigger>
          <TooltipContent>切换深色模式</TooltipContent>
        </Tooltip>

        <span className="chip chip-quiet">{version || '—'}</span>
        <Badge variant={ready ? 'success' : 'error'} className="rounded-full! h-5.5! px-2.5! gap-1.5!">
          <span className="status-dot" data-state={ready ? 'ok' : 'error'} />
          {ready ? 'Ready' : 'Not Ready'}
        </Badge>
      </div>
    </header>
  )
}
