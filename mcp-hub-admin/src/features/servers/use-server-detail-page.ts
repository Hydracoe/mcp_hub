import { useEffect, useMemo, useRef, useState, type SetStateAction } from 'react'
import { useBlocker, useNavigate, useParams } from 'react-router-dom'

import {
  useCategories,
  useDeleteServer,
  useOverview,
  useServer,
  useSetServerEnabled,
  useTestServer,
  useUpdateServer,
} from '@/lib/api/queries'
import { toast, toastError, toastSuccess } from '@/lib/toast'
import type { ProbeResult } from '@/lib/api/types'

import {
  buildServerRequest,
  createInitialForm,
  keyFormatError,
  type ServerFormState,
} from './detail/server-form-model'

/** Server 详情页的状态与动作。表单只在 server.id 变化时重建（轮询重取不会冲掉编辑）；
 *  脏检查比较「表单还原的请求体」而非逐字段，避免漏掉改回原值 / 加空 header。 */
export function useServerDetailPage() {
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
  /** 最近一次成功保存的请求体快照。保存后以此为准判脏——refetch 回来的
   *  server 只用于重建 initialJson（它只在换 server 时重算）。 */
  const [savedJson, setSavedJson] = useState<string | null>(null)

  // ServerForm 的 setForm 期望非 null 状态；这里做一层窄化封装
  const setForm = (u: SetStateAction<ServerFormState>) =>
    setFormRaw(u as SetStateAction<ServerFormState | null>)

  useEffect(() => {
    if (server && initId.current !== server.id) {
      initId.current = server.id
      setForm(createInitialForm(server))
      setProbe(server.probe)
      setSavedJson(null)
    }
  }, [server])

  // 脏检查：把当前表单与基线（上次保存的快照，缺省为按 server 还原的请求体）比较，
  // 未改动则保存按钮 disabled
  const initialJson = useMemo(
    () => (server ? JSON.stringify(buildServerRequest(createInitialForm(server))) : null),
    // 仅在 server 身份变化时重算，编辑态不被 refetch 覆盖
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [server?.id],
  )
  const dirty =
    !!form &&
    !!initialJson &&
    JSON.stringify(buildServerRequest(form)) !== (savedJson ?? initialJson)

  // 离开未保存提示
  const blocker = useBlocker(dirty)

  /** Key 格式先在本地挡一道：后端会拒（400），但那是一条英文原文。 */
  const checkKey = (f: ServerFormState) => {
    const err = keyFormatError(f.key)
    if (!err) return true
    toast.error('Key 不合法', { description: err })
    return false
  }

  const handleSave = () => {
    if (!form) return
    if (!checkKey(form)) return
    const req = buildServerRequest(form)
    update.mutate(req, {
      onSuccess: () => {
        // 保存成功即重置脏基线：离开页面不再误报未保存。
        setSavedJson(JSON.stringify(req))
        toastSuccess('已保存', '配置已更新')
      },
      onError: (e) => toastError(e),
    })
  }

  const handleSaveAndTest = async () => {
    if (!form || !id) return
    if (!checkKey(form)) return
    const req = buildServerRequest(form)
    try {
      await update.mutateAsync(req)
      setSavedJson(JSON.stringify(req))
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

  return {
    overview,
    server,
    categories,
    form,
    setForm,
    probe,
    dirty,
    /** 三态：加载中 / 服务端报错 / 就绪。页面按它选分支，不必自己拼条件。 */
    status: serverQ.isError ? 'error' : serverQ.isLoading || !server || !form ? 'loading' : 'ready',
    loadError: serverQ.error,
    refetch: serverQ.refetch,
    pending: {
      update: update.isPending,
      test: test.isPending,
      del: del.isPending,
      setEnabled: setEnabled.isPending,
    },
    blocker,
    handleSave,
    handleSaveAndTest,
    handleTest,
    handleDelete,
    handleToggleEnabled,
  }
}
