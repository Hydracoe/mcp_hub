import {
  Dialog,
  DialogBody,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Badge } from '@/components/ui/badge'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { Icon } from '@/components/common/Icon'
import { CodeBlock } from '@/components/common/CodeBlock'
import { Dash } from '@/components/common/KeyValue'
import type { ToolEntry } from '@/lib/api/types'

import { parseSchema } from './schema-model'

/** inputSchema 查看器。用 Radix 受控渲染，不打开就不在 DOM 里。 */
export function ToolSchemaDialog({
  tool,
  open,
  onOpenChange,
}: {
  tool: ToolEntry | null
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const schema = tool?.inputSchema
  const { fields, supported } = parseSchema(schema)
  const json = schema ? JSON.stringify(schema, null, 2) : ''

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent size="lg">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <Icon name="code" size={16} className="text-icon-muted" />
            Input Schema
          </DialogTitle>
          <DialogDescription className="font-mono">
            {tool?.exposedName ?? ''} · 下游原名 {tool?.name ?? ''}
          </DialogDescription>
        </DialogHeader>

        <DialogBody>
          <section className="space-y-2">
            <h3 className="text-[10.5px] font-semibold tracking-[0.06em] text-icon-muted uppercase">参数摘要</h3>
            {!supported ? (
              <p className="rounded-lg border border-separator bg-fill-tertiary px-3 py-2 text-[12px] text-muted-foreground">
                这个 schema 的结构超出了一层属性的范围（含 array / object / 联合类型），
                表单模式无法完整表达，试调时会直接给 JSON 输入框。
              </p>
            ) : fields.length === 0 ? (
              <p className="text-[12.5px] text-muted-foreground">该工具不需要参数。</p>
            ) : (
              <Table className="min-w-[420px]">
                <TableHeader>
                  <TableRow>
                    <TableHead>参数</TableHead>
                    <TableHead className="w-24">类型</TableHead>
                    <TableHead className="w-20">必填</TableHead>
                    <TableHead>说明</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {fields.map((f) => (
                    <TableRow key={f.name}>
                      <TableCell className="font-mono text-foreground">{f.name}</TableCell>
                      <TableCell className="font-mono text-muted-foreground">
                        {f.kind === 'enum' ? `enum(${f.enumValues?.length ?? 0})` : f.kind}
                      </TableCell>
                      <TableCell>
                        {f.required ? <Badge variant="error">必填</Badge> : <span className="text-muted-foreground">选填</span>}
                      </TableCell>
                      <TableCell className="text-muted-foreground">
                        {f.description ?? <Dash />}
                        {f.kind === 'enum' ? (
                          <div className="mt-1 flex flex-wrap gap-1">
                            {f.enumValues?.map((v) => (
                              <code key={v} className="code-chip">
                                {v}
                              </code>
                            ))}
                          </div>
                        ) : null}
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            )}
          </section>

          <section className="space-y-2">
            <h3 className="text-[10.5px] font-semibold tracking-[0.06em] text-icon-muted uppercase">原始 JSON</h3>
            {json ? (
              <CodeBlock code={json} language="json" maxHeight={320} />
            ) : (
              <p className="text-[12.5px] text-muted-foreground">下游未声明 inputSchema。</p>
            )}
          </section>
        </DialogBody>
      </DialogContent>
    </Dialog>
  )
}
