import { useEffect, useMemo, useState } from 'react'
import { useSearchParams } from 'react-router-dom'

import { useCredentialMutations, useCredentials, useOverview, useServers } from '@/lib/api/queries'
import type { CredentialView } from '@/lib/api/types'

/** 密钥池页状态。所选 Server 写进 URL 的 ?server=，便于跳转与分享；密钥池是 per-server 资源。 */
export function useCredentialsPage() {
  const [params, setParams] = useSearchParams()
  const { data: overview } = useOverview()
  const { data: servers } = useServers()

  const serverId = params.get('server') ?? ''
  const serverList = servers?.items ?? []
  const current = serverList.find((s) => s.id === serverId) ?? null

  // Server 列表回来前 URL 可能为空，默认落到第一个 Server，避免用户看到空白。
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

  /* 汇总是 per-server 口径，就地算成一行统计。 */
  const counts = useMemo(
    () => ({
      usable: items.filter((c) => c.state === 'active' && c.enabled).length,
      cooling: items.filter((c) => c.state === 'cooling').length,
      disabled: items.filter((c) => !c.enabled || c.state === 'disabled').length,
      unavailable: items.filter((c) => c.state !== 'active').length,
    }),
    [items],
  )

  const openCreate = () => {
    setEditing(null)
    setFormOpen(true)
  }
  const openEdit = (c: CredentialView) => {
    setEditing(c)
    setFormOpen(true)
  }

  const selectServer = (id: string) => setParams({ server: id }, { replace: true })

  return {
    overview,
    serverId,
    current,
    keyPool,
    items,
    total: data?.total ?? items.length,
    list: { isLoading, error, refetch },
    mutations,
    counts,
    dialogs: {
      formOpen,
      setFormOpen,
      editing,
      importOpen,
      setImportOpen,
      openCreate,
      openEdit,
    },
    selectServer,
  }
}
