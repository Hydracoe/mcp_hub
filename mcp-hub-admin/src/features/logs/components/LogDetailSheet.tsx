import {
  Sheet,
  SheetBody,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { Badge } from '@/components/ui/badge'
import { CodeBlock } from '@/components/common/CodeBlock'
import { ErrorKindBadge } from '@/components/common/StatusBadge'
import { MonoText } from '@/components/common/KeyValue'
import { Dash } from '@/components/common/KeyValue'
import { Icon } from '@/components/common/Icon'
import { formatDateTimeMs, formatDuration, safePrettyJson } from '@/lib/format'
import type { LogItem } from '@/lib/api/types'

interface Props {
  log: LogItem | null
  open: boolean
  onOpenChange: (open: boolean) => void
}

/** 按错误分类给一句排查方向。后端没下发，这里前端补一句最常说的话。 */
const ERROR_HINTS: Record<string, string> = {
  DNS_FAIL: '检查 Server 的 URL / 域名解析，确认下游主机可达。',
  CONN_REFUSED: '确认下游进程已启动并在监听目标端口。',
  TLS_FAIL: '检查下游证书与协议（http / https）是否匹配。',
  TIMEOUT: '下游响应超时，可尝试调大 timeoutMs 或排查下游慢调用。',
  AUTH_REQUIRED: '该下游需要鉴权，请在密钥里补充对应的 header。',
  RATE_LIMITED: '触发限流，密钥进入冷却并将自动恢复；必要时调大 rpm。',
  QUOTA_EXHAUSTED: '配额耗尽，需更换或补充可用密钥。',
  NO_USABLE_KEY: '密钥池无可用密钥（全部冷却 / 弃用），请补充可用密钥。',
  SERVER_DRAINING: 'Server 正在下线（draining），等待在途调用收尾。',
  CANCELED: '调用被取消（客户端中断或上游取消）。',
  NOT_FOUND: '工具 / 资源不存在，确认 exposedName 是否正确。',
  UPSTREAM_ERROR: '下游返回错误，查看下方「错误详情」定位。',
  NOT_MCP: '下游不是 MCP 服务，检查 transport 配置。',
  PROTOCOL_MISMATCH: '协议版本不匹配，确认 protocolVersion。',
  CAPABILITY_MISSING: '下游缺少所需能力（如 tools）。',
  SPAWN_FAILED: '下游进程启动失败，检查 command / args / env。',
  EXIT_EARLY: '下游进程提前退出，查看 stderr。',
  HANDSHAKE_TIMEOUT: '握手超时，确认下游 MCP 实现正常。',
  UNKNOWN: '未知错误，查看下方「错误详情」。',
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div>
      <dt className="mb-1 text-xs uppercase tracking-wider text-muted-foreground">{label}</dt>
      <dd>{children}</dd>
    </div>
  )
}

export function LogDetailSheet({ log, open, onOpenChange }: Props) {
  return (
    // 用受控 Sheet：关闭即卸载，避免常驻 DOM 占 tab 顺序并在深色模式闪一下。
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent side="right" className="gap-0 p-0">
        {log ? (
          <>
            <SheetHeader>
              <SheetTitle>日志详情</SheetTitle>
              <SheetDescription className="font-mono text-[11.5px]">{log.id}</SheetDescription>
            </SheetHeader>
            <SheetBody>
              <dl className="space-y-4">
                <Field label="完整时间">
                  <MonoText>{formatDateTimeMs(log.ts)}</MonoText>
                </Field>
                <Field label="Server">
                  <MonoText>{log.serverKey}</MonoText>
                </Field>
                <Field label="工具">
                  <MonoText>{log.exposedName}</MonoText>
                </Field>
                <Field label="耗时">
                  <span className="font-mono text-sm text-foreground">
                    {formatDuration(log.durationMs)}
                  </span>
                </Field>
                <Field label="状态">
                  {log.status === 'ok' ? (
                    <Badge variant="success" className="rounded-full! font-mono">
                      ok
                    </Badge>
                  ) : (
                    <Badge variant="error" className="rounded-full! font-mono">
                      error
                    </Badge>
                  )}
                </Field>
                <Field label="错误分类">
                  <ErrorKindBadge kind={log.errorKind} />
                </Field>
                <Field label="错误详情">
                  {log.error ? (
                    <pre className="code-block whitespace-pre-wrap p-3 text-[12px]">
                      {log.error}
                    </pre>
                  ) : (
                    <Dash />
                  )}
                </Field>
                {/* apiKeyId：上游调网关；credentialId：网关调下游。 */}
                <Field label="调用方（上游密钥）">
                  {log.apiKeyName || log.apiKeyId ? (
                    <div className="space-y-0.5">
                      {log.apiKeyName ? (
                        <div className="text-foreground">{log.apiKeyName}</div>
                      ) : (
                        // 无名字快照时如实说明
                        <div className="text-muted-foreground">未记录名字</div>
                      )}
                      {log.apiKeyId ? <MonoText>{log.apiKeyId}</MonoText> : null}
                    </div>
                  ) : (
                    <span className="text-muted-foreground">
                      匿名调用（该次调用发生时端点未启用接入鉴权）
                    </span>
                  )}
                </Field>
                <Field label="下游密钥">
                  {log.credentialId ? (
                    <div className="space-y-0.5">
                      {log.credentialLabel ? (
                        <div className="text-foreground">{log.credentialLabel}</div>
                      ) : null}
                      <MonoText>{log.credentialId}</MonoText>
                    </div>
                  ) : (
                    <span className="text-muted-foreground">—</span>
                  )}
                </Field>
              </dl>

              <div>
                <div className="mb-1 flex items-center gap-2">
                  <span className="text-xs uppercase tracking-wider text-muted-foreground">参数</span>
                  {/* 后端已截断，原样说明，避免伪装成完整内容 */}
                  <span className="text-[11px] text-muted-foreground">（已由网关截断）</span>
                </div>
                <CodeBlock code={safePrettyJson(log.args)} language="json" maxHeight={200} />
              </div>

              <div>
                <div className="mb-1 flex items-center gap-2">
                  <span className="text-xs uppercase tracking-wider text-muted-foreground">返回结果</span>
                  <span className="text-[11px] text-muted-foreground">（已由网关截断）</span>
                </div>
                <CodeBlock code={safePrettyJson(log.result)} language="json" maxHeight={200} />
              </div>

              {log.errorKind && ERROR_HINTS[log.errorKind] ? (
                <div className="flex items-start gap-2 rounded-lg border border-brand/25 bg-brand/8 px-3 py-2.5">
                  <Icon name="lightbulb" size={15} className="mt-0.5 shrink-0 text-brand-strong" />
                  <div>
                    <p className="text-[11px] font-semibold uppercase tracking-wider text-brand-strong">
                      排查方向
                    </p>
                    <p className="mt-0.5 text-[12.5px] leading-relaxed text-foreground">
                      {ERROR_HINTS[log.errorKind]}
                    </p>
                  </div>
                </div>
              ) : null}
            </SheetBody>
          </>
        ) : null}
      </SheetContent>
    </Sheet>
  )
}
