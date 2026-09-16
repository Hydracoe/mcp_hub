import { Button } from '@/components/ui/button'
import { FilterField, FilterGrid, FilterPanel } from '@/components/common/FilterPanel'
import { Icon } from '@/components/common/Icon'
import { PageLayout } from '@/components/layout/PageLayout'
import { Section } from '@/components/common/Section'
import { formatInt } from '@/lib/format'

import { ServerPicker } from './components/ServerPicker'
import { CredentialsTable } from './components/CredentialsTable'
import { CredentialsEmptyState } from './components/CredentialsEmptyState'
import { CredentialsTableFoot, KeyPoolSummary } from './components/KeyPoolSummary'
import { CredentialFormDialog } from './components/CredentialFormDialog'
import { CredentialImportDialog } from './components/CredentialImportDialog'
import { useCredentialsPage } from './use-credentials-page'

/** 密钥池：网关拿去调下游的凭据，按 Server 隔离。
 *  （「接入密钥」页是上游调网关用的，两回事。） */
export function CredentialsPage() {
  const {
    overview,
    serverId,
    current,
    keyPool,
    items,
    total,
    list,
    mutations,
    counts,
    dialogs,
    selectServer,
  } = useCredentialsPage()

  const writable = Boolean(serverId) && keyPool

  return (
    <PageLayout
      title="密钥池"
      version={overview?.version}
      actions={
        <>
          <Button
            variant="secondary"
            size="sm"
            onClick={() => dialogs.setImportOpen(true)}
            disabled={!writable}
          >
            <Icon name="upload" size={13} />
            批量导入
          </Button>
          <Button variant="primary" size="sm" onClick={dialogs.openCreate} disabled={!writable}>
            <Icon name="plus" size={13} />
            新增密钥
          </Button>
        </>
      }
    >
      <Section
        title="密钥查询"
        hint={
          <span className="inline-flex items-center gap-1.5 text-xs text-muted-foreground">
            <Icon name="funnel" size={14} /> 密钥按 Server 隔离
          </span>
        }
      >
        <FilterPanel footer={items.length > 0 ? <KeyPoolSummary counts={counts} /> : undefined}>
          <FilterGrid columns={2}>
            <FilterField label="Server">
              <ServerPicker value={serverId || null} onChange={selectServer} className="w-full" />
            </FilterField>
          </FilterGrid>
        </FilterPanel>
      </Section>

      <Section
        className="mt-4"
        title="密钥明细"
        hint={
          <span className="text-[11.5px] text-muted-foreground">
            共 {formatInt(total)} 把
          </span>
        }
        actions={
          <Button
            variant="ghost"
            size="icon-sm"
            aria-label="刷新"
            onClick={() => void list.refetch()}
          >
            <Icon name="refresh-cw" size={14} />
          </Button>
        }
      >
        <CredentialsTable
          items={items}
          mutations={mutations}
          onEdit={dialogs.openEdit}
          frame={{
            loading: Boolean(serverId) && list.isLoading,
            error:
              serverId && list.error
                ? {
                  title: '密钥池加载失败',
                  error: list.error,
                  onRetry: () => void list.refetch(),
                }
                : undefined,
            isEmpty: !writable || items.length === 0,
            empty: (
              <CredentialsEmptyState
                noServer={!serverId}
                singleKey={!keyPool}
                onCreate={dialogs.openCreate}
                onImport={() => dialogs.setImportOpen(true)}
              />
            ),
            footer: <CredentialsTableFoot counts={counts} />,
          }}
        />
      </Section>

      {serverId ? (
        <CredentialFormDialog
          open={dialogs.formOpen}
          onOpenChange={dialogs.setFormOpen}
          initial={dialogs.editing}
          mutations={mutations}
        />
      ) : null}

      {serverId && current ? (
        <CredentialImportDialog
          open={dialogs.importOpen}
          onOpenChange={dialogs.setImportOpen}
          mutations={mutations}
        />
      ) : null}
    </PageLayout>
  )
}
