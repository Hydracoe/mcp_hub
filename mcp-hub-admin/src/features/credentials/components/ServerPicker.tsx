import { useMemo } from 'react'

import { Combobox, type ComboboxOption } from '@/components/common/Combobox'
import { useServers } from '@/lib/api/queries'
import { cn } from '@/lib/utils'

interface ServerPickerProps {
  value: string | null
  onChange: (id: string) => void
  className?: string
}

/** 选要管哪个 server 的密钥池，结果由上层写回 URL（?server=）。 */
export function ServerPicker({ value, onChange, className }: ServerPickerProps) {
  const { data: servers, isLoading } = useServers()

  /* Server 可能几十上百个，走带搜索的下拉，key 与 name 都参与匹配。 */
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
      className={cn('w-70', className)}
      placeholder={isLoading ? '加载中…' : '选择 Server'}
      searchPlaceholder="搜索 key 或名称…"
      emptyText="没有匹配的 Server"
      aria-label="选择 Server"
    />
  )
}
