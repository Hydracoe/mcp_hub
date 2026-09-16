import { useMemo } from 'react'

import { Combobox, type ComboboxOption } from '@/components/common/Combobox'
import { useServers } from '@/lib/api/queries'

interface ServerPickerProps {
  value: string | null
  onChange: (id: string) => void
}

/** 选择要管理哪个 server 的密钥池。结果由上层写回 URL（?server=）。 */
export function ServerPicker({ value, onChange }: ServerPickerProps) {
  const { data: servers, isLoading } = useServers()

  /* Server 数量是外部数据，可能几十上百 —— 走带搜索的 Combobox，
     展示名放右侧灰字，搜索时 key 与 name 都参与匹配。 */
  const options = useMemo<ComboboxOption[]>(
    () =>
      (servers?.items ?? []).map((s) => ({
        value: s.id,
        label: s.key,
        hint: s.name,
      })),
    [servers],
  )

  return (
    <Combobox
      value={value}
      onChange={onChange}
      options={options}
      className="w-[280px]"
      placeholder={isLoading ? '加载中…' : '选择 Server'}
      searchPlaceholder="搜索 key 或名称…"
      emptyText="没有匹配的 Server"
      aria-label="选择 Server"
    />
  )
}
