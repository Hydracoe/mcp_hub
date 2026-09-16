import { useEffect, useMemo, useState } from 'react'
import { useSearchParams } from 'react-router-dom'

import { Button } from '@/components/ui/button'
import { Icon } from '@/components/common/Icon'
import { PageLayout } from '@/components/layout/PageLayout'
import { Section } from '@/components/common/Section'
import { EmptyState } from '@/components/common/States'
import { useCredentialMutations, useCredentials, useOverview, useServers } from '@/lib/api/queries'
import { formatInt } from '@/lib/format'
import type { CredentialView } from '@/lib/api/types'

import { ServerPicker } from './components/ServerPicker'
import { CredentialsTable } from './components/CredentialsTable'
import { CredentialFormDialog } from './components/CredentialFormDialog'
import { CredentialImportDialog } from './components/CredentialImportDialog'

/**
 * 密钥池。
 *
 * ⚠ 设计稿没有这一页（它把密钥管理做成了 Server 列表里的弹框），
 * 但密钥池是 per-server 的资源，所以这里必须先把「管哪个 Server」
 * 定下来 —— 选择结果写进 URL 的 ?server=，这样从 Server 详情页
 * 或列表页跳过来能直接落在同一把 Server 上，链接也可分享。
 */
export function CredentialsPage() {
  const [params, setParams] = useSearchParams()
  const { data: overview } = useOverview()
  const { data: servers } = useServers()

  const serverId = params.get('server') ?? ''
  const serverList = servers?.items ?? []
  const current = serverList.find((s) => s.id === serverId) ?? null

  // Server 列表回来之前 URL 里可能是空值；默认落到第一个 Server，
  // 免得用户看到一片空白还要自己找下拉。
  useEffect(() => {
    if (!serverId && serverList.length > 0) {
      setParams({ server: serverList[0].id }, { replace: true })
    }
  }, [serverId, serverList, setParams])

  const { data, isLoading, error, refetch } = useCredentials(serverId || undefined)
  const mutations = useCredentialMutations(serverId)

  const [formOpen, setFormOpen] = useState(false)
  const [editing, setEditing] = useState<CredentialView | null>(null)
  const [importOpen, setImportOpen] = useState(false)

  const items = data?.items ?? []
  const keyPool = data?.keyPool ?? current?.keyPool ?? true

  /* 汇总就地算（per-server 口径），收敛成一行统计字而不是五张卡：
     「不可用 = 冷却 + 已弃用」在卡片形态里是两张重复的卡。 */
  const usableCount = useMemo(
    () => items.filter((c) => c.state === 'active' && c.enabled).length,
    [items],
  )
  const disabledCount = useMemo(
    () => items.filter((c) => !c.enabled || c.state === 'disabled').length,
    [items],
  )
  const coolingCount = useMemo(() => items.filter((c) => c.state === 'cooling').length, [items])

  const openCreate = () => {
    setEditing(null)
    setFormOpen(true)
  }
  const openEdit = (c: CredentialView) => {
    setEditing(c)
    setFormOpen(true)
  }

  /* 三种「表里没东西」是不同的事，文案分开；但都保留表头 ——
     用户至少能看到密钥明细由哪几列构成。 */
  const emptyState = !serverId ? (
    <EmptyState
      icon="key-round"
      title="先选择一个 Server"
      description="密钥池是按 Server 隔离的：每把密钥只对它所归属的下游生效，轮换与冷却也各自独立。"
    />
  ) : !keyPool ? (
    /* keyPool === false 是「这个下游天生不需要密钥池」，不是「坏了」。
       设计稿原版在这里会显示一张空表，读起来像是数据丢了。 */
    <EmptyState
      icon="shield-check"
      title="该 Server 使用单密钥模式"
      description={
        <>
          它的下游凭据直接写在连接的 <code className="code-chip">headers</code> 里，不参与密钥池轮换。
          需要换成多密钥轮换的话，先在 Server 详情页确认鉴权方式支持密钥池（stdio 下游不支持）。
        </>
      }
    />
  ) : (
    <EmptyState
      icon="key-round"
      title="还没有配置密钥"
      description="新增单把密钥，或从一行一把的文本批量导入。"
      action={
        <div className="flex items-center gap-2">
          <Button variant="secondary" size="sm" onClick={() => setImportOpen(true)}>
            批量导入
          </Button>
          <Button variant="primary" size="sm" onClick={openCreate}>
            新增密钥
          </Button>
        </div>
      }
    />
  )

  return (
    <PageLayout
      title="密钥池"
      version={overview?.version}
      ready={overview?.ready}
    >
      {/* 条件行：左「管哪个 Server」，右「池子现状 + 两个入口」。
          ServerPicker 的选中项已经表明了是谁，不再重复渲染 key/name；
          动作按钮也不进顶部标题行（与工具市场同一套约定）。 */}
      <div className="mb-5 flex flex-wrap items-center gap-3">
        <ServerPicker
          value={serverId || null}
          onChange={(id) => setParams({ server: id }, { replace: true })}
        />

        <div className="ml-auto flex flex-wrap items-center gap-x-4 gap-y-1 text-[12px]">
          {items.length > 0 ? (
            <span className="flex items-center gap-x-3 gap-y-1 text-[11.5px] text-muted-foreground">
              <span>
                可用 <span className="tabular-nums text-success-strong">{formatInt(usableCount)}</span>
              </span>
              <span>
                冷却 <span className="tabular-nums text-warning-strong">{formatInt(coolingCount)}</span>
              </span>
              <span>
                停用 <span className="tabular-nums text-error-strong">{formatInt(disabledCount)}</span>
              </span>
            </span>
          ) : null}
          <Button variant="secondary" size="sm" onClick={() => setImportOpen(true)} disabled={!serverId || !keyPool}>
            <Icon name="upload" size={13} />
            批量导入
          </Button>
          <Button variant="primary" size="sm" onClick={openCreate} disabled={!serverId || !keyPool}>
            <Icon name="plus" size={13} />
            新增密钥
          </Button>
        </div>
      </div>

      <Section
        title="密钥明细"
        hint={
          <span className="text-[11.5px] text-muted-foreground">
            共 {formatInt(data?.total ?? items.length)} 把 · rpm 是滑动窗口用量，窗口结束自动清零
          </span>
        }
        actions={
          <Button variant="ghost" size="icon-sm" aria-label="刷新" onClick={() => void refetch()}>
            <Icon name="refresh-cw" size={14} />
          </Button>
        }
      >
        {/* 外壳 / 状态位 / 页脚注释统一收进 TableFrame。 */}
        <CredentialsTable
          items={items}
          mutations={mutations}
          onEdit={openEdit}
          frame={{
            loading: Boolean(serverId) && isLoading,
            error:
              serverId && error
                ? { title: '密钥池加载失败', error, onRetry: () => void refetch() }
                : undefined,
            isEmpty: !serverId || !keyPool || items.length === 0,
            empty: emptyState,
            footer: (
              <div className="flex flex-wrap items-center gap-x-4 gap-y-1 border-t border-separator px-4 py-2.5 text-[11.5px] text-muted-foreground">
                <span>
                  TPM 未计量的密钥显示「未计量」而不是 0 —— 下游没回 usage 时，0 会被误读成「没用过」。
                </span>
                <span className="tabular-nums">
                  不可用合计 {formatInt(items.filter((c) => c.state !== 'active').length)} 把
                  {items.some((c) => !c.tpmMetered) ? ' · 含未计量项' : ''}
                </span>
              </div>
            ),
          }}
        />
      </Section>

      {serverId ? (
        <CredentialFormDialog
          open={formOpen}
          onOpenChange={setFormOpen}
          initial={editing}
          mutations={mutations}
        />
      ) : null}

      {serverId && current ? (
        <CredentialImportDialog
          open={importOpen}
          onOpenChange={setImportOpen}
          mutations={mutations}
        />
      ) : null}
    </PageLayout>
  )
}
