import { useEffect, useState } from 'react'

import { Icon } from '@/components/common/Icon'
import { Spinner } from '@/components/common/States'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Switch } from '@/components/ui/switch'
import { onUnauthorized } from '@/lib/api/client'
import { useOverview } from '@/lib/api/queries'
import { useAuthStore } from '@/stores/auth-store'
import { cn } from '@/lib/utils'

/** 管理鉴权闸门：后端没开 --api-key 时全部放行、不渲染任何东西；
 *  只在真的收到 401 才介入——比启动就探 /api/overview 更准，避免抖动时误弹。 */
export function AuthGate({ children }: { children: React.ReactNode }) {
  const rejected = useAuthStore((s) => s.rejected)
  const markRejected = useAuthStore((s) => s.markRejected)
  const [open, setOpen] = useState(false)

  // client 层收到 401 会广播，这里接住并弹解锁层。
  useEffect(() => {
    const off = onUnauthorized(markRejected)
    return () => {
      off()
    }
  }, [markRejected])

  useEffect(() => {
    if (rejected) setOpen(true)
  }, [rejected])

  return (
    <>
      {children}
      <UnlockDialog open={open} onOpenChange={setOpen} />
    </>
  )
}

/** 解锁 / 重试弹窗。 */
export function UnlockDialog({
  open,
  onOpenChange,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const apiKey = useAuthStore((s) => s.apiKey)
  const rejected = useAuthStore((s) => s.rejected)
  const unlock = useAuthStore((s) => s.unlock)
  const clear = useAuthStore((s) => s.clear)
  const [draft, setDraft] = useState(apiKey)
  const [remember, setRemember] = useState(true)
  const [checking, setChecking] = useState(false)

  // 弹窗打开时把草稿重置为已存值，避免上一次的错误输入残留。
  useEffect(() => {
    if (open) {
      setDraft(apiKey)
      setChecking(false)
    }
  }, [open, apiKey])

  // 校验是真打一次 /api/overview：本地无法验证令牌，长度判断只会把错误推迟。
  const { refetch } = useOverview({ enabled: false })

  async function submit() {
    const key = draft.trim()
    if (!key) return
    setChecking(true)
    unlock(remember ? key : '')
    const res = await refetch()
    setChecking(false)
    if (!res.isError) onOpenChange(false)
  }

  return (
    <Dialog open={open} onOpenChange={(next) => !checking && onOpenChange(next)}>
      <DialogContent size="sm">
        {/* 无头无脚的纯表单弹框：正文内边距与 DialogBody 对齐（px-5 py-4）。 */}
        <div className="space-y-5 px-5 py-4">
          <div className="flex flex-col items-center gap-2 pt-1 text-center">
            <span
              className={cn(
                'grid size-10 place-items-center rounded-xl',
                rejected ? 'bg-error-surface text-error' : 'bg-selection text-brand-strong',
              )}
            >
              <Icon name={rejected ? 'shield-alert' : 'shield-check'} size={20} />
            </span>
            <p className="text-[13px] font-semibold text-foreground">
              {rejected ? '凭据不被接受' : '此网关已启用管理鉴权'}
            </p>
            <p className="max-w-[42ch] text-[11.5px] leading-relaxed text-muted-foreground">
              {rejected ? (
                <>
                  服务端返回 <code className="code-chip">401 unauthorized</code>，请核对 API Key。
                </>
              ) : (
                <>输入 API Key 以继续。密钥仅保存在本浏览器，不会发往第三方。</>
              )}
            </p>
          </div>

          <div>
            <label className="form-label" htmlFor="auth-api-key">
              API Key
            </label>
            <Input
              id="auth-api-key"
              type="password"
              autoComplete="off"
              autoFocus
              value={draft}
              placeholder="••••••••••••"
              aria-invalid={rejected || undefined}
              className={rejected ? 'border-error! ring-[3.5px]! ring-error/14!' : undefined}
              onChange={(e) => setDraft(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter') void submit()
              }}
            />
            {rejected ? (
              <p className="mt-1.5 inline-flex items-center gap-1 text-[11.5px] text-error-strong">
                <Icon name="circle-alert" size={14} />
                本机服务不做失败锁定，可继续重试
              </p>
            ) : (
              <p className="form-hint">
                对应网关启动参数 <code className="code-chip">--api-key</code>
              </p>
            )}
          </div>

          <div className="flex items-center justify-between gap-2">
            <label className="flex cursor-pointer items-center gap-2 text-[11.5px] text-muted-foreground">
              <Switch
                checked={remember}
                onCheckedChange={setRemember}
                aria-label="本浏览器记住"
                className="scale-[.82]!"
                style={{ transformOrigin: 'left center' }}
              />
              本浏览器记住
            </label>
            <div className="flex items-center gap-2">
              {apiKey ? (
                <Button variant="ghost" onClick={clear}>
                  清除已存密钥
                </Button>
              ) : null}
              <Button variant="primary" disabled={checking || !draft.trim()} onClick={() => void submit()}>
                {checking ? <Spinner /> : null}
                {rejected ? '重试' : '解锁'}
              </Button>
            </div>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  )
}
