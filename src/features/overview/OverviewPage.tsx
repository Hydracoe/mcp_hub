import { PageLayout } from '@/components/layout/PageLayout'
import { Section } from '@/components/common/Section'
import { CardSkeleton, ErrorState } from '@/components/common/States'
import { useOverview } from '@/lib/api/queries'

import { StatCards } from './components/StatCards'
import { RuntimeOverview } from './components/RuntimeOverview'
import { KeyPoolSection } from './components/KeyPoolSection'

export function OverviewPage() {
  const { data: overview, isError, error, refetch } = useOverview()

  if (isError && !overview) {
    return (
      <PageLayout title="概览 Dashboard">
        <ErrorState error={error} onRetry={() => void refetch()} />
      </PageLayout>
    )
  }

  return (
    <PageLayout
      title="概览 Dashboard"
      version={overview?.version}
      ready={overview?.ready}
    >
      <Section title="网关总览" hint={<span className="text-[11.5px] text-muted-foreground">实时刷新 · 每 15s</span>}>
        {!overview ? (
          <CardSkeleton count={4} />
        ) : (
          <div className="flex flex-col gap-5">
            <StatCards overview={overview} />
            <RuntimeOverview overview={overview} />
          </div>
        )}
      </Section>

      <Section
        title="密钥池异常"
        hint={<span className="text-[11.5px] text-muted-foreground">仅显示冷却 / 停用的密钥 · 按 Server 分组，点击展开</span>}
      >
        {!overview ? (
          <CardSkeleton count={1} />
        ) : (
          <KeyPoolSection details={overview.keyDetails} />
        )}
      </Section>
    </PageLayout>
  )
}
