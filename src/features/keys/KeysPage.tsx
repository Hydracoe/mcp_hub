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

/** 接入密钥：上游客户端调 /mcp 时出示、由网关签发的凭据（全局一份清单）。
 *  与侧栏「密钥池」不同——那按 Server 隔离，是网关调下游用的。 */
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
              </div>
            ),
          }}
        />
      </Section>

      <ApiKeyCreateDialog open={createOpen} onOpenChange={setCreateOpen} mutations={mutations} />
    </PageLayout>
  )
}

/** 鉴权态势卡片：/mcp 的鉴权开关不是勾选框，而是「库里有没有 key」这个事实
 *  （见 httpd.mcpAuth）——建第一把 / 删最后一把会突然改变客户端表现。 */
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
