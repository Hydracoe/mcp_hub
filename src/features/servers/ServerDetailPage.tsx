import { PageLayout } from '@/components/layout/PageLayout'
import { Icon } from '@/components/common/Icon'
import { Button } from '@/components/ui/button'
import { ErrorState, Spinner } from '@/components/common/States'

import { ServerForm } from './detail/ServerForm'
import { ProbePanel } from './detail/components/ProbePanel'
import { RuntimeMeta } from './detail/components/RuntimeMeta'
import { DangerZone } from './detail/components/DangerZone'
import { ToolsInventory } from './detail/components/ToolsInventory'
import { ToolChangesCard } from './detail/components/ToolChangesCard'
import { CredentialsSummary } from './detail/components/CredentialsSummary'
import { DetailSkeleton } from './detail/components/DetailSkeleton'
import { UnsavedChangesDialog } from './detail/components/UnsavedChangesDialog'
import { useServerDetailPage } from './use-server-detail-page'

/** Server 详情：左栏编辑表单，右栏 sticky 探活/运行时/危险区，底部全宽只读清单（工具与密钥）。 */
export function ServerDetailPage() {
  const {
    overview,
    server,
    categories,
    form,
    setForm,
    probe,
    dirty,
    status,
    loadError,
    refetch,
    pending,
    blocker,
    handleSaveAndTest,
    handleTest,
    handleDelete,
    handleToggleEnabled,
  } = useServerDetailPage()

  if (status === 'error') {
    return (
      <PageLayout
        title="Server 详情"
        backTo="/servers"
        version={overview.data?.version}
      >
        {/* /servers/:id 不存在时给「未找到」而非崩溃 */}
        <ErrorState title="未找到该 Server" error={loadError} onRetry={() => void refetch()} />
      </PageLayout>
    )
  }

  if (status === 'loading' || !server || !form) {
    return (
      <PageLayout
        title={server?.name ?? 'Server 详情'}
        backTo="/servers"
        version={overview.data?.version}
      >
        <DetailSkeleton />
      </PageLayout>
    )
  }

  return (
    <PageLayout
      title={server.name}
      backTo="/servers"
      version={overview.data?.version}
      fixed
      contentWidth="wide"
      actions={
        <>
          <Button
            variant="secondary"
            size="sm"
            onClick={handleSaveAndTest}
            disabled={!dirty || pending.update}
          >
            {pending.update ? <Spinner size={13} /> : <Icon name="check" size={14} />}
            保存并测试
          </Button>
        </>
      }
    >
      <div className="grid grid-cols-1 gap-6 lg:grid-cols-3">
        {/* 左：编辑表单 */}
        <div className="lg:col-span-2 min-w-0">
          <ServerForm form={form} setForm={setForm} categories={categories} />
        </div>

        {/* 右：sticky 面板 */}
        <div className="lg:col-span-1 min-w-0">
          <div className="sticky-panel space-y-4">
            <ProbePanel probe={probe} testing={pending.test} onTest={handleTest} />
            <RuntimeMeta server={server} />
            <DangerZone
              server={server}
              onDelete={handleDelete}
              deleting={pending.del}
              onToggleEnabled={handleToggleEnabled}
              enabling={pending.setEnabled}
            />
          </div>
        </div>
      </div>

      {/* 全宽区块：工具清单 + 变更记录 + 密钥池摘要 */}
      <div className="mt-6 space-y-6">
        <ToolsInventory server={server} />
        <ToolChangesCard serverId={server.id} />
        <CredentialsSummary server={server} />
      </div>

      <UnsavedChangesDialog
        open={blocker.state === 'blocked'}
        onDismiss={() => blocker.reset?.()}
        onConfirm={() => blocker.proceed?.()}
      />
    </PageLayout>
  )
}
