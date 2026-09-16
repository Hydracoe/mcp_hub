import { Link } from 'react-router-dom'
import {
  Dialog,
  DialogBody,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Button } from '@/components/ui/button'
import { Icon } from '@/components/common/Icon'
import { ServerStatusBadge, TransportTag, CredentialStateBadge } from '@/components/common/StatusBadge'
import { useCredentials } from '@/lib/api/queries'
import { formatInt, formatPercent, shortTarget } from '@/lib/format'
import type { CredentialView, ServerSummary } from '@/lib/api/types'

interface Props {
  server: ServerSummary | null
  open: boolean
  onOpenChange: (open: boolean) => void
}

/**
 * 密钥管理弹框：只读摘要 + 每把密钥的状态/指纹/用量。
 * 新增/编辑密钥不在本弹框（那是密钥池页职责），这里只给入口链接。
 * ⚠ 设计稿原版弹窗常驻 DOM（display:none），此处改 Radix 受控 open。
 */
export function ServerCredentialsDialog({ server, open, onOpenChange }: Props) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent size="xl">
        {server ? <CredsBody server={server} /> : null}
      </DialogContent>
    </Dialog>
  )
}

function CredsBody({ server }: { server: ServerSummary }) {
  const { data, isLoading } = useCredentials(server.id)
  const keyPool = data?.keyPool ?? server.keyPool

  return (
    <>
      <DialogHeader>
        <DialogTitle className="flex items-center gap-2">
          <Icon name="key-round" size={16} className="text-icon-muted" />
          密钥管理
        </DialogTitle>
        <DialogDescription className="font-mono">
          {server.key} · GET /api/servers/{server.key}/credentials
        </DialogDescription>
      </DialogHeader>

      <DialogBody>
        <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5 text-[11.5px] text-muted-foreground">
          <TransportTag transport={server.transport} />
          <ServerStatusBadge status={server.status} />
          <span className="font-mono">{shortTarget(server.target)}</span>
          {keyPool ? (
            <span>
              密钥 <b className="text-foreground tabular-nums">{server.usableKeyCount} / {server.keyCount}</b> 在用
            </span>
          ) : null}
          {typeof server.keySuccessRate === 'number' ? (
            <span>
              成功率 <b className="text-foreground tabular-nums">{formatPercent(server.keySuccessRate)}</b>
            </span>
          ) : null}
        </div>

        {!keyPool ? (
          <p className="flex items-start gap-2 rounded-lg border border-separator bg-fill-tertiary p-3 text-[12px] text-foreground">
            <Icon name="info" size={15} className="mt-0.5 shrink-0 text-icon-muted" />
            单密钥模式：该 Server 不走密钥池（stdio 下游或直连无需鉴权），没有可轮换的密钥，密钥页显示「无需」。
            新增 / 编辑密钥请到密钥池页。
          </p>
        ) : isLoading ? (
          <p className="py-8 text-center text-[12px] text-muted-foreground">加载密钥列表…</p>
        ) : (
          <ul className="divide-y divide-separator rounded-lg border border-separator">
            {(data?.items ?? []).map((c) => (
              <CredRow key={c.id} cred={c} />
            ))}
          </ul>
        )}
      </DialogBody>

      <DialogFooter>
        <span className="text-[11.5px] text-muted-foreground">只读视图 · 新增/编辑请到密钥池页</span>
        <Button variant="outline" size="sm" asChild>
          <Link to={`/credentials?server=${server.id}`}>
            <Icon name="external-link" size={13} />
            在密钥池中管理
          </Link>
        </Button>
      </DialogFooter>
    </>
  )
}

function CredRow({ cred }: { cred: CredentialView }) {
  return (
    <li className="flex flex-wrap items-center gap-x-4 gap-y-1.5 px-3 py-2.5">
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          <span className="truncate font-mono text-[12px] text-foreground" title={cred.label}>
            {cred.label}
          </span>
          {cred.fingerprint ? (
            <span className="shrink-0 font-mono text-[11px] text-muted-foreground">···{cred.fingerprint}</span>
          ) : null}
        </div>
        {cred.tpmMetered ? (
          <p className="mt-0.5 font-mono text-[11px] text-muted-foreground tabular-nums">
            {formatInt(cred.rpmUsed)}/{formatInt(cred.rpmLimit)} rpm · {formatInt(cred.tpmUsed)}/{formatInt(cred.tpmLimit)} tpm
          </p>
        ) : (
          <p className="mt-0.5 font-mono text-[11px] text-muted-foreground">未计量</p>
        )}
      </div>
      <CredentialStateBadge state={cred.state} disabledKind={cred.disabledKind} enabled={cred.enabled} />
    </li>
  )
}
