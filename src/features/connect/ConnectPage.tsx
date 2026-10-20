import { Link } from 'react-router-dom'

import { PageLayout } from '@/components/layout/PageLayout'
import { Section } from '@/components/common/Section'
import { CodeBlock } from '@/components/common/CodeBlock'
import { CopyButton } from '@/components/common/CopyButton'
import { Icon } from '@/components/common/Icon'
import { CardSkeleton, ErrorState } from '@/components/common/States'
import { useApiKeys, useConnect, useOverview } from '@/lib/api/queries'
import type { ConnectResponse } from '@/lib/api/types'

import { EndpointGrid } from './components/EndpointGrid'
import { ClientGuide } from './components/ClientGuide'
import { ConnectTest } from './components/ConnectTest'

/** 把后端 {名称:url} 折成客户端 mcpServers。需鉴权时连 headers 占位符一起给
 *  （放占位符而非真实密钥——明文只在创建时交付一次，本页不该拿到）。 */
function buildMcpServers(servers: Record<string, string>, keyRequired: boolean) {
  if (!keyRequired) return servers
  const out: Record<string, { url: string; headers: Record<string, string> }> = {}
  for (const [name, url] of Object.entries(servers)) {
    out[name] = { url, headers: { Authorization: 'Bearer <你的接入密钥>' } }
  }
  return out
}

function ScaleChips({ connect }: { connect: ConnectResponse }) {
  return (
    <div className="flex flex-wrap items-center gap-2">
      <span className="inline-flex items-center gap-1 rounded-md bg-muted px-2 py-1 text-xs text-foreground">
        <Icon name="wrench" size={13} className="text-icon-muted" />
        {connect.toolCount} 个工具
      </span>
      <span className="inline-flex items-center gap-1 rounded-md bg-muted px-2 py-1 text-xs text-foreground">
        <Icon name="server" size={13} className="text-icon-muted" />
        {connect.serverCount} 个 Server
      </span>
    </div>
  )
}

export function ConnectPage() {
  const { data: overview } = useOverview()
  const { data: connect, error, refetch } = useConnect()
  const { data: keys } = useApiKeys()

  /* 端点鉴权开关 = 库里有没有 key（见 httpd.mcpAuth），配置示例随之变化。 */
  const keyRequired = (keys?.total ?? 0) > 0
  const usableKeys = keys?.items.filter((k) => k.enabled).length ?? 0

  const configJson = connect
    ? JSON.stringify({ mcpServers: buildMcpServers(connect.mcpServers, keyRequired) }, null, 2)
    : ''

  return (
    <PageLayout
      title="接入指引"
      version={overview?.version}
    >
      {error ? (
        <Section title="端点列表">
          <ErrorState title="接入信息加载失败" error={error} onRetry={() => void refetch()} />
        </Section>
      ) : !connect ? (
        <Section title="端点列表">
          <CardSkeleton count={4} />
        </Section>
      ) : (
        <>
          <Section>
            <p className="text-sm text-muted-foreground">{connect.summary}</p>
            {/* 规模数属于「接入概况」，是读的信息不是按钮，跟着摘要走 */}
            <div className="mt-3">
              <ScaleChips connect={connect} />
            </div>
          </Section>

          <Section
            title="端点列表"
            hint={<span className="text-xs text-muted-foreground">网关对外暴露的接入端点</span>}
          >
            <EndpointGrid connect={connect} />
          </Section>

          <Section title="一键接入配置">
            <div className="ui-panel p-6">
              <div className="mb-4 flex items-center justify-between">
                <h3 className="text-base font-semibold text-foreground">客户端配置</h3>
                <CopyButton value={configJson} label="复制 JSON" />
              </div>
              <CodeBlock code={configJson} language="json" maxHeight={320} />
              {keyRequired ? (
                <p className="mt-3 text-xs text-muted-foreground">
                  端点已开启接入鉴权（{usableKeys} 把密钥可用），所以配置里多了{' '}
                  <code className="code-chip">headers</code> 一段 —— 把{' '}
                  <code className="code-chip">&lt;你的接入密钥&gt;</code> 换成你在
                  <Link to="/keys" className="mx-0.5 underline underline-offset-2 hover:text-foreground">
                    接入密钥
                  </Link>
                  页创建时保存下来的那串明文。
                </p>
              ) : (
                <p className="mt-3 text-xs text-muted-foreground">
                  复制后粘贴到你的 AI 客户端配置文件中（mcpServers 字段）。当前端点不鉴权，
                  配置里只需要 URL；一旦创建接入密钥，这里会自动补上 headers。
                </p>
              )}
            </div>
          </Section>

          <Section
            title="客户端接入指引"
            hint={<span className="text-xs text-muted-foreground">3 个常用 AI 客户端</span>}
          >
            <ClientGuide mcpEndpoint={connect.mcpEndpoint} />
          </Section>

          <Section title="注意事项">
            <div className="ui-panel p-5">
              {/* 后端给的原话，不自行改编 */}
              <p className="mb-3 rounded-md border border-separator bg-fill-tertiary px-3 py-2 text-sm text-foreground">
                {connect.note}
              </p>
              <ul className="space-y-3">
                <li className="flex items-start gap-2.5 text-sm text-foreground">
                  <Icon name="check" size={16} className="mt-0.5 shrink-0 text-success-strong" />
                  <span>
                    工具名带 <code className="code-chip">serverKey__</code> 前缀标识来源，如{' '}
                    <code className="code-chip">web-search__search</code>
                  </span>
                </li>
                <li className="flex items-start gap-2.5 text-sm text-foreground">
                  <Icon name="check" size={16} className="mt-0.5 shrink-0 text-success-strong" />
                  <span>某下游 Server 不可用时，其工具仍在聚合列表中，但调用会返回带原因的失败结果</span>
                </li>
                <li className="flex items-start gap-2.5 text-sm text-foreground">
                  <Icon name="check" size={16} className="mt-0.5 shrink-0 text-success-strong" />
                  <span>凭据热更新：修改密钥不需要重建连接，下一次调用自动使用新凭据</span>
                </li>
                <li className="flex items-start gap-2.5 text-sm text-foreground">
                  <Icon name="check" size={16} className="mt-0.5 shrink-0 text-success-strong" />
                  <span>
                    接入密钥：在 <Link to="/keys" className="underline underline-offset-2">接入密钥</Link>{' '}
                    页创建，客户端调 <code className="code-chip">/mcp</code> 时带{' '}
                    <code className="code-chip">Authorization: Bearer</code> 或{' '}
                    <code className="code-chip">X-API-Key</code>。库里只要存在过密钥就强制校验，
                    一把都没有时端点开放 —— 删掉最后一把即可退回。
                  </span>
                </li>
                <li className="flex items-start gap-2.5 text-sm text-foreground">
                  <Icon name="check" size={16} className="mt-0.5 shrink-0 text-success-strong" />
                  <span>
                    管理凭据是另一回事：网关以 <code className="code-chip">--api-key</code> 启动时，
                    访问管理 API 与进入本后台需要该令牌（由{' '}
                    <code className="code-chip">AuthGate</code> 弹解锁框，令牌只存在本机
                    localStorage 的一个键里）。两者互不替代 —— 拿接入密钥进不了后台，
                    拿管理令牌也调不了 <code className="code-chip">/mcp</code>。
                  </span>
                </li>
              </ul>
            </div>
          </Section>

          <Section title="测试连接">
            <div className="ui-panel p-5">
              <ConnectTest mcpEndpoint={connect.mcpEndpoint} />
            </div>
          </Section>
        </>
      )}
    </PageLayout>
  )
}
