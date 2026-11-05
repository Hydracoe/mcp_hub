import { Component, type ErrorInfo, type ReactNode } from 'react'

import { ErrorState } from '@/components/common/States'

interface Props {
  children: ReactNode
  /** 变化时自动复位 —— 路由一跳转就重试，否则一次失败会粘住整个会话。 */
  resetKey?: string
}

interface State {
  error: Error | null
}

/** 路由级错误边界。旧 chunk 被换掉会让 import() 404、整棵树白屏，
 *  这里兜住并给「重新加载」入口。React 19 仍只支持类组件做错误边界。 */
export class RouteErrorBoundary extends Component<Props, State> {
  state: State = { error: null }

  static getDerivedStateFromError(error: Error): State {
    return { error }
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    // 保留现场：chunk 404 与真正的渲染崩溃，排查方式完全不同。
    console.error('[route] 渲染失败', error, info.componentStack)
  }

  componentDidUpdate(prev: Props) {
    if (prev.resetKey !== this.props.resetKey && this.state.error) {
      this.setState({ error: null })
    }
  }

  render() {
    if (!this.state.error) return this.props.children

    return (
      <div className="app-content">
        <ErrorState
          title="页面加载失败"
          error={this.state.error}
          onRetry={() => window.location.reload()}
        />
      </div>
    )
  }
}
