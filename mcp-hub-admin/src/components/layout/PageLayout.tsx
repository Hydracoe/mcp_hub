import type { ReactNode } from 'react'

import { Toolbar } from './Toolbar'
import { cn } from '@/lib/utils'

export interface PageLayoutProps {
  title: string
  backTo?: string
  /** 页面级功能按钮，渲染在内容区顶部而不进导航栏（导航栏只放跨页面稳定元素）。 */
  actions?: ReactNode
  version?: string
  /** 内容区容器：默认限宽 1320px；`wide` 用于自带分栏的详情页（1240px）。 */
  contentWidth?: 'default' | 'wide'
  /** 内容区固定高度、页面自己滚动（Server 详情等固定布局）。 */
  fixed?: boolean
  className?: string
  children: ReactNode
}

/** 页面骨架：工具栏 + 内容区。标题、版本 chip、连接徽标等跨页面稳定元素收在这里。 */
export function PageLayout({
  title,
  backTo,
  actions,
  version,
  contentWidth = 'default',
  fixed = false,
  className,
  children,
}: PageLayoutProps) {
  /* 页面按钮落在内容区首行，右对齐。空数组/undefined 时不占高度。 */
  const actionBar = actions ? <div className="page-actions">{actions}</div> : null

  return (
    <>
      <Toolbar title={title} backTo={backTo} version={version} />
      {fixed ? (
        /* 这层 .app-scroll 不能省：.app-main.is-fixed 是 overflow:hidden，
           少了它详情页内容会被直接裁掉且无法滚动。 */
        <div className="app-scroll">
          <div className={cn('app-content-wide', className)}>
            {actionBar}
            {children}
          </div>
        </div>
      ) : (
        <div
          className={cn(
            contentWidth === 'wide' ? 'app-content-wide' : 'app-content',
            className,
          )}
        >
          {actionBar}
          {children}
        </div>
      )}
    </>
  )
}
