import type { ReactNode } from 'react'

/** 详情表单的小节外壳。间距与描边是页面的视觉基准，抽出来避免四个小节各抄一遍。 */
export function FormSection({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="form-section">
      <div className="form-section-header">{title}</div>
      <div className="form-section-body space-y-4">{children}</div>
    </section>
  )
}
