import { useState } from 'react'

import { Button } from '@/components/ui/button'
import { Switch } from '@/components/ui/switch'
import { TableCell, TableHead, TableRow } from '@/components/ui/table'
import { TableFrame } from '@/components/common/TableFrame'
import type { TableFrameProps } from '@/components/common/TableFrame'
import { Icon } from '@/components/common/Icon'
import { CredentialStateBadge } from '@/components/common/StatusBadge'
import { ConfirmDialog } from '@/components/common/ConfirmDialog'
import { Dash } from '@/components/common/KeyValue'
import {
  formatCountdown,
  formatDuration,
  formatInt,
  formatPercent,
  formatRelative,
} from '@/lib/format'
import { toastError, toastSuccess } from '@/lib/toast'
import type { CredentialView } from '@/lib/api/types'
import type { useCredentialMutations } from '@/lib/api/queries'

type Mutations = ReturnType<typeof useCredentialMutations>

interface CredentialsTableProps {
  items: CredentialView[]
  /** 外壳、状态位与页脚统一交给 TableFrame。 */
  frame?: Omit<TableFrameProps, 'head' | 'colCount' | 'children'>
  mutations: Mutations
  onEdit: (c: CredentialView) => void
}

const COLS = ['标签', '指纹', 'Header Keys', '状态', '冷却', '配额', '调度', '统计', '操作']

export function CredentialsTable({ items, frame, mutations, onEdit }: CredentialsTableProps) {
  const [deleteId, setDeleteId] = useState<string | null>(null)
  const [resetId, setResetId] = useState<string | null>(null)
  const [testing, setTesting] = useState<string | null>(null)
  const [testResult, setTestResult] = useState<Record<string, string>>({})

  const runTest = async (c: CredentialView) => {
    setTesting(c.id)
    setTestResult((p) => ({ ...p, [c.id]: '' }))
    try {
      const r = await mutations.test.mutateAsync(c.id)
      const verdict = r.ok ? '通过' : '未通过'
      const extra = r.error ? ` · ${r.error}` : r.hint ? ` · ${r.hint}` : ''
      setTestResult((p) => ({ ...p, [c.id]: `${verdict}${extra}` }))
    } catch (e) {
      toastError(e)
      setTestResult((p) => ({ ...p, [c.id]: '测试失败' }))
    } finally {
      setTesting(null)
    }
  }

  const confirmDelete = async () => {
    if (!deleteId) return
    try {
      await mutations.remove.mutateAsync(deleteId)
      toastSuccess('已删除密钥')
    } catch (e) {
      toastError(e)
    } finally {
      setDeleteId(null)
    }
  }

  const confirmReset = async () => {
    if (!resetId) return
    try {
      await mutations.resetQuota.mutateAsync(resetId)
      toastSuccess('已重置配额')
    } catch (e) {
      toastError(e)
    } finally {
      setResetId(null)
    }
  }

  return (
    <>
      <TableFrame
        {...frame}
        colCount={COLS.length}
        tableClassName="min-w-[1120px]"
        head={
          <TableRow>
            {COLS.map((h) => (
              <TableHead key={h}>{h}</TableHead>
            ))}
          </TableRow>
        }
      >
        {items.map((c) => (
          <CredentialRow
            key={c.id}
            c={c}
            mutations={mutations}
            onEdit={onEdit}
            onTest={runTest}
            testing={testing === c.id}
            testResult={testResult[c.id]}
            onDelete={() => setDeleteId(c.id)}
            onReset={() => setResetId(c.id)}
          />
        ))}
      </TableFrame>

      <ConfirmDialog
        open={Boolean(deleteId)}
        onOpenChange={(o) => !o && setDeleteId(null)}
        destructive
        title="删除密钥"
        description="删除后该密钥不再参与轮换，历史日志中的 credentialId 仍保留。此操作不可撤销。"
        confirmText="确认删除"
        pending={mutations.remove.isPending}
        onConfirm={confirmDelete}
      />
      <ConfirmDialog
        open={Boolean(resetId)}
        onOpenChange={(o) => !o && setResetId(null)}
        title="重置配额"
        description="将这把密钥的 rpm 当前用量清零，冷却也会一并解除。"
        confirmText="重置"
        pending={mutations.resetQuota.isPending}
        onConfirm={confirmReset}
      />
    </>
  )
}

interface RowProps {
  c: CredentialView
  mutations: Mutations
  onEdit: (c: CredentialView) => void
  onTest: (c: CredentialView) => void
  testing: boolean
  testResult?: string
  onDelete: () => void
  onReset: () => void
}

function CredentialRow({ c, mutations, onEdit, onTest, testing, testResult, onDelete, onReset }: RowProps) {
  const remain = c.coolingUntil ? c.coolingUntil - Date.now() : c.coolingForMs
  const rpm = `${formatInt(c.rpmUsed)} / ${c.rpmLimit ? formatInt(c.rpmLimit) : '不限'}`

  return (
    <TableRow>
      <TableCell>
        <div className="font-medium text-foreground">{c.label}</div>
        {c.configured ? null : <span className="cell-sub text-warning-strong">未配置</span>}
      </TableCell>
      <TableCell>
        <span className="font-mono text-muted-foreground">
          {c.fingerprint ? `···${c.fingerprint}` : '—'}
        </span>
      </TableCell>
      <TableCell>
        <span className="font-mono">
          {c.headerKeys?.length ? c.headerKeys.join(', ') : <Dash />}
        </span>
      </TableCell>
      <TableCell>
        {/* 状态列只做「读」：徽章 + 停用原因。启停是动作，放操作列，
            和测试/编辑/删除并排 —— 混在状态格里会被当成状态的一部分。 */}
        <CredentialStateBadge state={c.state} disabledKind={c.disabledKind} enabled={c.enabled} />
        {c.disabledKind ? (
          <div className="mt-1 cell-sub text-error-strong">{c.disabledKind}</div>
        ) : null}
      </TableCell>
      <TableCell>
        {c.state === 'cooling' ? (
          <div className="font-mono text-warning-strong">{formatCountdown(remain)}</div>
        ) : (
          <Dash />
        )}
        {c.consecutiveLimited ? (
          <div className="cell-sub text-muted-foreground">连续限流 {c.consecutiveLimited}</div>
        ) : null}
      </TableCell>
      <TableCell>
        <div className="font-mono tabular-nums">{rpm}</div>
      </TableCell>
      <TableCell>
        <div className="font-mono tabular-nums">轮询 {formatInt(c.picks)}</div>
        <div className="cell-sub text-muted-foreground">{formatRelative(c.lastUsedAt)}</div>
        {c.lastError ? (
          <div className="mt-0.5 max-w-[160px] truncate cell-sub text-error-strong" title={c.lastError}>
            {c.lastError}
          </div>
        ) : null}
      </TableCell>
      <TableCell>
        <div className="font-mono tabular-nums">
          {formatInt(c.callCount)} / {formatInt(c.failCount)}
        </div>
        <div className="font-mono tabular-nums text-muted-foreground">
          {formatPercent(c.successRate)} · {formatDuration(c.p95Ms)}
        </div>
      </TableCell>
      <TableCell>
        <div className="flex items-center gap-1">
          {/* 启停开关：从状态列挪过来放最前 —— 它是这一行最高频的动作。 */}
          <Switch
            checked={c.enabled}
            disabled={mutations.setEnabled.isPending}
            onCheckedChange={(v) => mutations.setEnabled.mutate({ cid: c.id, enabled: v })}
            aria-label="启用/停用"
          />
          <Button
            size="icon-sm"
            variant="ghost"
            aria-label="测试"
            disabled={testing}
            onClick={() => onTest(c)}
          >
            <Icon name="flask-conical" size={13} />
          </Button>
          <Button size="icon-sm" variant="ghost" aria-label="重置配额" onClick={onReset}>
            <Icon name="rotate-cw" size={13} />
          </Button>
          <Button size="icon-sm" variant="ghost" aria-label="编辑" onClick={() => onEdit(c)}>
            <Icon name="pen-line" size={13} />
          </Button>
          <Button size="icon-sm" variant="ghost" aria-label="删除" onClick={onDelete}>
            <Icon name="trash-2" size={13} />
          </Button>
        </div>
        {testing ? (
          <div className="mt-1 cell-sub text-muted-foreground">测试中…</div>
        ) : testResult ? (
          <div className="mt-1 max-w-[200px] cell-sub text-foreground" title={testResult}>
            {testResult}
          </div>
        ) : null}
      </TableCell>
    </TableRow>
  )
}
