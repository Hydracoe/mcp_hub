import { useState } from 'react'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Icon } from '@/components/common/Icon'
import { PageLayout } from '@/components/layout/PageLayout'
import { Section } from '@/components/common/Section'
import { EmptyState } from '@/components/common/States'
import { useApiKeyMutations, useApiKeys, useOverview } from '@/lib/api/queries'
import { formatInt } from '@/lib/format'

import { KeysTable } from './components/KeysTable'
import { ApiKeyCreateDialog } from './components/ApiKeyCreateDialog'

/**
 * 接入密钥。
 *
 * 上游 AI 客户端调 `/mcp` 时出示的凭据，由网关自己签发。
 * 与侧边栏里的「密钥池」是两回事：那是网关**拿去调下游**的凭据，
 * 按 Server 隔离；这里是**上游调网关**的凭据，全局一份清单。
 *
 * ⚠ 这一页在 PRD 里没有独立章节（FR-4.x 只讲了接入配置的复制），
 * 但没有它 `/mcp` 就是一条死路：网关一旦建过 key 就强制鉴权，
 * 而用户只能手敲 curl 才能建出第一把。
 */
export function KeysPage() {
  const { data: overview } = useOverview()
  const { data, isLoading, error, refetch } = useApiKeys()
  const mutations = useApiKeyMutations()
  const [createOpen, setCreateOpen] = useState(false)

  const items = data?.items ?? []
  const total = data?.total ?? items.length
  const enabledCount = items.filter((k) => k.enabled).length

  const emptyState = (
    <EmptyState
      icon="key-round"
      title="还没有接入密钥"
      description="建一把发给调用方，客户端就能带上它访问 /mcp。注意：创建第一把密钥会让端点从「不鉴权」切换成「强制鉴权」。"
      action={
        <Button variant="primary" size="sm" onClick={() => setCreateOpen(true)}>
          新建密钥
        </Button>
      }
    />
  )

  return (
    <PageLayout
      title="接入密钥"
      version={overview?.version}
      ready={overview?.ready}
      actions={
        <>
          <Button variant="ghost" size="icon-sm" aria-label="刷新" onClick={() => void refetch()}>
            <Icon name="refresh-cw" size={14} />
          </Button>
          <Button variant="primary" size="sm" onClick={() => setCreateOpen(true)}>
            <Icon name="plus" size={13} />
            新建密钥
          </Button>
        </>
      }
    >
      <div className="mb-5">
        <AuthPosture total={total} enabled={enabledCount} />
      </div>

      <Section
        title="密钥清单"
        hint={
          <span className="text-[11.5px] text-muted-foreground">
            共 {formatInt(total)} 把 · {formatInt(enabledCount)} 把可用 · 明文只在创建时交付一次，之后只显示前缀
          </span>
        }
      >
        <KeysTable
          items={items}
          mutations={mutations}
          frame={{
            loading: isLoading,
            error: error ? { title: '密钥清单加载失败', error, onRetry: () => void refetch() } : undefined,
            isEmpty: items.length === 0,
            empty: emptyState,
            footer: (
              <div className="flex flex-wrap items-center gap-x-4 gap-y-1 border-t border-separator px-4 py-2.5 text-[11.5px] text-muted-foreground">
                <span>
                  网关只保存密钥的 sha256，明文落不了库 —— 所以它只显示一次，丢了只能删掉重建。
                </span>
                <span>「最近使用」由调用方每次成功鉴权时回写，最多每分钟落一次库。</span>
              </div>
            ),
          }}
        />
      </Section>

      <ApiKeyCreateDialog open={createOpen} onOpenChange={setCreateOpen} mutations={mutations} />
    </PageLayout>
  )
}

/**
 * 鉴权态势。
 *
 * 这张卡片存在的理由：`/mcp` 的鉴权开关不是一个勾选框，而是
 * "库里有没有 key"这个事实本身（见 httpd.mcpAuth）。用户没有
 * 这层解释，就无法理解为什么建完第一把密钥、或者删掉最后一把，
 * 客户端的表现会突然变样。三种状态都说「现在谁能调」和「怎么做会变」，
 * 不用「强制鉴权 / 端点锁死」这类只有后端视角才懂的词。
 */
function AuthPosture({ total, enabled }: { total: number; enabled: number }) {
  if (total === 0) {
    return (
      <Alert variant="warning">
        <Icon name="shield-alert" size={16} />
        <AlertTitle>任何人都能调用工具，还没有开启密钥校验</AlertTitle>
        <AlertDescription>
          一把密钥都没建，凡是能访问到网关的人都能直接调用全部工具。创建第一把密钥后，
          调用就必须带密钥了 —— 这个动作本身就是开启校验的开关。
        </AlertDescription>
      </Alert>
    )
  }

  if (enabled === 0) {
    return (
      <Alert variant="destructive">
        <Icon name="shield-alert" size={16} />
        <AlertTitle>密钥全部停用，现在谁都调不了工具</AlertTitle>
        <AlertDescription>
          已建过密钥，校验就一直开着；而此刻没有一把可用，所以所有请求都会被拒绝 ——
          连不带密钥的也一样。启用其中一把即可恢复；想退回「无需密钥」，只能把它们全部删除。
        </AlertDescription>
      </Alert>
    )
  }

  return (
    <Alert variant="info">
      <Icon name="shield-check" size={16} />
      <AlertTitle>调用工具需要出示密钥</AlertTitle>
      <AlertDescription>
        {formatInt(enabled)} / {formatInt(total)} 把密钥可用。客户端请求带上它就能调用，
        不带会被拒绝。请求头用 <code className="code-chip">Authorization: Bearer &lt;密钥&gt;</code>{' '}
        或 <code className="code-chip">X-API-Key: &lt;密钥&gt;</code>，完整配置示例见「接入指引」。
      </AlertDescription>
    </Alert>
  )
}
