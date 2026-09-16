import { type SetStateAction, useEffect, useMemo, useRef, useState } from 'react'
import { useBlocker, useNavigate, useParams } from 'react-router-dom'

import { PageLayout } from '@/components/layout/PageLayout'
import { Icon } from '@/components/common/Icon'
import { Button } from '@/components/ui/button'
import { ErrorState, Spinner, CardSkeleton } from '@/components/common/States'
import { Skeleton } from '@/components/ui/skeleton'
import { ConfirmDialog } from '@/components/common/ConfirmDialog'
import { toastError, toastSuccess } from '@/lib/toast'
import {
  useCategories,
  useDeleteServer,
  useOverview,
  useServer,
  useSetServerEnabled,
  useTestServer,
  useUpdateServer,
} from '@/lib/api/queries'
import type { ProbeResult } from '@/lib/api/types'

import {
  buildServerRequest,
  createInitialForm,
  ServerForm,
  type ServerFormState,
} from './detail/ServerForm'
import { ProbePanel } from './detail/components/ProbePanel'
import { RuntimeMeta } from './detail/components/RuntimeMeta'
import { DangerZone } from './detail/components/DangerZone'
import { ToolsInventory } from './detail/components/ToolsInventory'
import { CredentialsSummary } from './detail/components/CredentialsSummary'

export function ServerDetailPage() {
  const { id } = useParams<{ id: string }>()
  const navigate = useNavigate()
  const overview = useOverview()
  const serverQ = useServer(id)
  const categoriesQ = useCategories()
  const server = serverQ.data
  const categories = categoriesQ.data?.items ?? []

  const update = useUpdateServer(id ?? '')
  const del = useDeleteServer()
  const setEnabled = useSetServerEnabled()
  const test = useTestServer()

  const [form, setFormRaw] = useState<ServerFormState | null>(null)
  const [probe, setProbe] = useState<ProbeResult | undefined>(undefined)
  const initId = useRef<string | null>(null)

  // ServerForm 的 setForm 期望非 null 状态；这里做一层窄化封装
  const setForm = (u: SetStateAction<ServerFormState>) =>
    setFormRaw(u as SetStateAction<ServerFormState | null>)

  useEffect(() => {
    if (server && initId.current !== server.id) {
      initId.current = server.id
      setForm(createInitialForm(server))
      setProbe(server.probe)
    }
  }, [server])

  // 脏检查：把当前表单与按 server 还原的请求体比较，未改动则保存按钮 disabled
  const initialReq = useMemo(
    () => (server ? buildServerRequest(createInitialForm(server)) : null),
    // 仅在 server 身份变化时重算，编辑态不被 refetch 覆盖
    [server?.id],
  )
  const dirty =
    !!form &&
    !!initialReq &&
    JSON.stringify(buildServerRequest(form)) !== JSON.stringify(initialReq)

  const blocker = useBlocker(dirty)

  /* ── 操作 ── */

  const handleSave = () => {
    if (!form) return
    update.mutate(buildServerRequest(form), {
      onSuccess: () => toastSuccess('已保存', '配置已更新'),
      onError: (e) => toastError(e),
    })
  }

  const handleSaveAndTest = async () => {
    if (!form || !id) return
    try {
      await update.mutateAsync(buildServerRequest(form))
      toastSuccess('已保存')
      await test.mutateAsync(id)
    } catch (e) {
      toastError(e)
    }
  }

  const handleTest = () => {
    if (!id) return
    test.mutate(id, {
      onSuccess: (d) => setProbe(d),
      onError: (e) => toastError(e),
    })
  }

  const handleDelete = () => {
    if (!id) return
    del.mutate(id, {
      onSuccess: () => {
        toastSuccess('已删除', 'Server 及其密钥池已移除')
        navigate('/servers')
      },
      onError: (e) => toastError(e),
    })
  }

  const handleToggleEnabled = () => {
    if (!server) return
    setEnabled.mutate(
      { id: server.id, enabled: !server.enabled },
      {
        onSuccess: () => toastSuccess(server.enabled ? '已停用' : '已启用'),
        onError: (e) => toastError(e),
      },
    )
  }

  /* ── 加载 / 错误 / 不存在 ── */

  if (serverQ.isError) {
    return (
      <PageLayout
        title="Server 详情"
        backTo="/servers"
        version={overview.data?.version}
        ready={overview.data?.ready}
      >
        {/* ⚠ 设计稿原版无「不存在」态；此处 /servers/:id 不存在时给「未找到」而非崩溃 */}
        <ErrorState title="未找到该 Server" error={serverQ.error} onRetry={() => serverQ.refetch()} />
      </PageLayout>
    )
  }

  if (serverQ.isLoading || !server || !form) {
    return (
      <PageLayout
        title={server?.name ?? 'Server 详情'}
        backTo="/servers"
        version={overview.data?.version}
        ready={overview.data?.ready}
      >
        <div className="grid grid-cols-1 gap-6 lg:grid-cols-3">
          <div className="lg:col-span-2 min-w-0">
            {/* 这里是表单不是表格，不要拿表格骨架凑形状 ——
                行高与真实字段对不上，加载完会跳。 */}
            <div className="form-card space-y-5 p-5">
              {Array.from({ length: 8 }).map((_, i) => (
                <Skeleton key={i} className="h-9 w-full" />
              ))}
            </div>
          </div>
          <div className="lg:col-span-1 min-w-0 space-y-4">
            <CardSkeleton count={2} />
          </div>
        </div>
      </PageLayout>
    )
  }

  return (
    <PageLayout
      title={server.name}
      backTo="/servers"
      version={overview.data?.version}
      ready={overview.data?.ready}
      fixed
      contentWidth="wide"
      actions={
        <>
          <Button variant="ghost" size="sm" onClick={handleTest} disabled={test.isPending}>
            {test.isPending ? <Spinner size={13} /> : <Icon name="activity" size={14} />}
            测试连接
          </Button>
          <Button
            variant="secondary"
            size="sm"
            onClick={handleSaveAndTest}
            disabled={!dirty || update.isPending}
          >
            {update.isPending ? <Spinner size={13} /> : <Icon name="check" size={14} />}
            保存并测试
          </Button>
          <Button
            variant="primary"
            size="sm"
            onClick={handleSave}
            disabled={!dirty || update.isPending}
          >
            {update.isPending ? <Spinner size={13} /> : <Icon name="check" size={14} />}
            保存
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
            <ProbePanel probe={probe} testing={test.isPending} onTest={handleTest} />
            <RuntimeMeta server={server} />
            <DangerZone
              server={server}
              onDelete={handleDelete}
              deleting={del.isPending}
              onToggleEnabled={handleToggleEnabled}
              enabling={setEnabled.isPending}
            />
          </div>
        </div>
      </div>

      {/* 全宽区块：工具清单 + 密钥池摘要 */}
      <div className="mt-6 space-y-6">
        <ToolsInventory server={server} />
        <CredentialsSummary server={server} />
      </div>

      {/* 离开未保存提示（缺口 D5：设计稿缺确认） */}
      <ConfirmDialog
        open={blocker.state === 'blocked'}
        onOpenChange={(o) => {
          if (!o) blocker.reset?.()
        }}
        title="有未保存的修改"
        description="离开此页将丢弃本次修改，确定继续吗？"
        confirmText="丢弃并离开"
        cancelText="留下"
        destructive
        onConfirm={() => blocker.proceed?.()}
      />
    </PageLayout>
  )
}
