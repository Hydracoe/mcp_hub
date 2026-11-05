import { Link } from 'react-router-dom'

import { Icon } from '@/components/common/Icon'
import { KeyValueGrid } from '@/components/common/KeyValue'
import { Badge } from '@/components/ui/badge'
import { formatInt } from '@/lib/format'
import type { ServerDetail } from '@/lib/api/types'

export interface CredentialsSummaryProps {
  server: ServerDetail
}

export function CredentialsSummary({ server }: CredentialsSummaryProps) {
  if (server.keyPool === false) {
    return (
      <div className="form-card">
        <div className="form-section-header">密钥池摘要</div>
        <div className="form-section-body">
          <div className="flex items-center gap-2 text-[12.5px]">
            <Badge variant="neutral">单密钥模式</Badge>
            <span className="text-muted-foreground">
              该下游沿用 <span className="font-mono">config.headers</span>，不走密钥池轮换。
            </span>
          </div>
          <Link
            to={`/credentials?server=${encodeURIComponent(server.id)}`}
            className="ui-btn ui-btn-ghost mt-3 h-7 px-2.5 text-[12px]"
          >
            <Icon name="key-round" size={14} />
            去密钥池页管理
          </Link>
        </div>
      </div>
    )
  }

  const c = server.credentials
  if (!c) {
    return (
      <div className="form-card">
        <div className="form-section-header">密钥池摘要</div>
        <div className="form-section-body text-[12.5px] text-muted-foreground">
          暂无密钥池摘要数据（可能尚未探活）。
        </div>
      </div>
    )
  }

  const items = [
    { key: 'total', label: '密钥总数', value: formatInt(c.total) },
    { key: 'usable', label: '可用', value: formatInt(c.usable), tone: 'success' as const },
    { key: 'cooling', label: '冷却中', value: formatInt(c.cooling), tone: 'warning' as const },
    { key: 'disabled', label: '已弃用', value: formatInt(c.disabled), tone: 'error' as const },
    { key: 'unusable', label: '不可用', value: formatInt(c.unusable), tone: 'error' as const },
  ]

  return (
    <div className="form-card">
      <div className="form-section-header flex items-center justify-between">
        <span>密钥池摘要（只读）</span>
        <Link
          to={`/credentials?server=${encodeURIComponent(server.id)}`}
          className="ui-btn ui-btn-ghost h-7 px-2.5 text-[12px]"
        >
          <Icon name="key-round" size={14} />
          去密钥池页管理
        </Link>
      </div>
      <div className="form-section-body">
        <KeyValueGrid items={items} columns={4} />
        {c.unusable > 0 ? (
          <p className="form-hint mt-2">
            <Icon name="triangle-alert" size={13} className="mr-1 inline-block align-[-2px]" />
            有 {formatInt(c.unusable)} 把密钥不可用（冷却或弃用），将不进入轮换。
          </p>
        ) : null}
      </div>
    </div>
  )
}
