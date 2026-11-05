import { Icon } from '@/components/common/Icon'
import { Badge } from '@/components/ui/badge'
import {
    BareTable,
    TableBody,
    TableCell,
    TableHead,
    TableHeader,
    TableRow,
    TableSkeletonRows,
    TableStateRow,
} from '@/components/ui/table'
import type { ToolChangeAction } from '@/lib/api/types'
import { useToolChanges } from '@/lib/api/queries'
import { formatDateTime, formatRelative } from '@/lib/format'

export interface ToolChangesCardProps {
    serverId: string
}

/** 工具变更记录：探活对账（增 / 删 / 改）与人工启停留痕，倒序最近 20 条。 */
export function ToolChangesCard({ serverId }: ToolChangesCardProps) {
    const { data, isLoading } = useToolChanges({ serverId, limit: 20 })
    const items = data?.items ?? []

    return (
        <div className="form-card">
            <div className="form-section-header">
                <span>工具变更记录</span>
            </div>
            <div className="form-section-body">
                <div className="ui-table-scroll">
                    <BareTable className="min-w-160">
                        <TableHeader>
                            <TableRow>
                                <TableHead>时间</TableHead>
                                <TableHead>工具</TableHead>
                                <TableHead>动作</TableHead>
                                <TableHead>来源</TableHead>
                                <TableHead>明细</TableHead>
                            </TableRow>
                        </TableHeader>
                        <TableBody>
                            {isLoading ? (
                                <TableSkeletonRows rows={5} cols={5} />
                            ) : items.length === 0 ? (
                                <TableStateRow colSpan={5}>
                                    <div className="px-6 py-8 text-center text-muted-foreground">
                                        暂无变更记录
                                    </div>
                                </TableStateRow>
                            ) : (
                                items.map((c) => (
                                    <TableRow key={c.id}>
                                        <TableCell className="whitespace-nowrap text-muted-foreground">
                                            <span title={formatDateTime(c.ts)}>{formatRelative(c.ts)}</span>
                                        </TableCell>
                                        <TableCell className="font-mono">{c.toolName}</TableCell>
                                        <TableCell>
                                            <Badge variant={ACTION_TONE[c.action]}>{ACTION_LABEL[c.action]}</Badge>
                                        </TableCell>
                                        <TableCell className="whitespace-nowrap text-muted-foreground">
                                            {c.actor === 'admin' ? '人工操作' : '探活对账'}
                                        </TableCell>
                                        <TableCell className="text-muted-foreground">{c.detail ?? '—'}</TableCell>
                                    </TableRow>
                                ))
                            )}
                        </TableBody>
                    </BareTable>
                </div>
                <p className="form-hint mt-2">
                    <Icon name="activity" size={13} className="mr-1 inline-block align-[-2px]" />
                    记录只追加：工具的新增 / 移除 / 描述变化由探活自动写入，启用 / 禁用记录人工动作。
                </p>
            </div>
        </div>
    )
}

const ACTION_LABEL: Record<ToolChangeAction, string> = {
    added: '新发现',
    removed: '已移除',
    updated: '已更新',
    enabled: '已启用',
    disabled: '已禁用',
}

const ACTION_TONE: Record<ToolChangeAction, 'success' | 'error' | 'warning' | 'neutral' | 'brand'> = {
    added: 'brand',
    removed: 'error',
    updated: 'warning',
    enabled: 'success',
    disabled: 'neutral',
}
