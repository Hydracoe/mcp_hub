import { Button } from '@/components/ui/button'
import { EmptyState } from '@/components/common/States'

interface Props {
  noServer: boolean
  /** 该下游走单密钥模式（`keyPool === false`）。 */
  singleKey: boolean
  onCreate: () => void
  onImport: () => void
}

/** 密钥表的三种空态必须分开：没选 Server、单密钥模式、池子真空着，混为一谈会误导用户。 */
export function CredentialsEmptyState({ noServer, singleKey, onCreate, onImport }: Props) {
  if (noServer) {
    return (
      <EmptyState
        icon="key-round"
        title="先选择一个 Server"
        description="密钥池是按 Server 隔离的：每把密钥只对它所归属的下游生效，轮换与冷却也各自独立。"
      />
    )
  }

  /* keyPool === false 是「天生不需要密钥池」，不是数据丢了。 */
  if (singleKey) {
    return (
      <EmptyState
        icon="shield-check"
        title="该 Server 使用单密钥模式"
        description={
          <>
            它的下游凭据直接写在连接的 <code className="code-chip">headers</code> 里，不参与密钥池轮换。
            需要换成多密钥轮换的话，先在 Server 详情页确认鉴权方式支持密钥池（stdio 下游不支持）。
          </>
        }
      />
    )
  }

  return (
    <EmptyState
      icon="key-round"
      title="还没有配置密钥"
      description="新增单把密钥，或从一行一把的文本批量导入。"
      action={
        <div className="flex items-center gap-2">
          <Button variant="secondary" size="sm" onClick={onImport}>
            批量导入
          </Button>
          <Button variant="primary" size="sm" onClick={onCreate}>
            新增密钥
          </Button>
        </div>
      }
    />
  )
}
