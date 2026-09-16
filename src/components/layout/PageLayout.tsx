import type { ReactNode } from 'react'

import { Toolbar } from './Toolbar'
import { cn } from '@/lib/utils'

export interface PageLayoutProps {
  title: string
  backTo?: string
  /**
   * 页面级功能按钮。
   *
   * **渲染在内容区顶部，不进导航栏。** 导航栏（`.app-toolbar`）只承载返回、
   * 标题、主题开关、版本与就绪徽标这些跨页面稳定的元素；页面按钮跟着
   * 侧边栏选中项变化，放上去会让导航栏变成第二排工具栏 —— 设计稿的
   * `.toolbar-trail` 里从来只有两枚 chip。
   */
  actions?: ReactNode
  version?: string
  ready?: boolean
  /** 内容区容器：默认限宽 1320px；`wide` 用于自带分栏的详情页（1240px）。 */
  contentWidth?: 'default' | 'wide'
  /** 内容区固定高度、页面自己滚动（Server 详情等固定布局）。 */
  fixed?: boolean
  className?: string
  children: ReactNode
}

/**
 * 页面骨架：工具栏 + 内容区。
 *
 * 每个页面只写内容。标题、版本 chip、就绪徽标这些
 * 10 个页面都一样的东西收在这里。
 */
export function PageLayout({
  title,
  backTo,
  actions,
  version,
  ready,
  contentWidth = 'default',
  fixed = false,
  className,
  children,
}: PageLayoutProps) {
  /* 页面按钮落在内容区首行，右对齐。空数组/undefined 时不占高度。 */
  const actionBar = actions ? <div className="page-actions">{actions}</div> : null

  return (
    <>
      <Toolbar title={title} backTo={backTo} version={version} ready={ready} />
      {fixed ? (
        /* ⚠ 设计稿原版把 .app-content-wide 直接挂在 .app-scroll 下面；
           之前这里少了 .app-scroll 这一层，而 .app-main.is-fixed 是
           overflow:hidden —— 详情页内容会被直接裁掉且无法滚动。 */
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
