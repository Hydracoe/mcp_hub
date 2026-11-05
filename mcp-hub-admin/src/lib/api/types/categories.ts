/* ─────────────────────── 分类 / 元数据 ─────────────────────── */

export interface Category {
  id: string
  name: string
  /** 英文名（slug），用于分类列表与筛选参数，不进对外工具名
   *  （对外工具名 = `<serverKey>__<toolName>`）。仅允许 [a-z0-9-]。 */
  slug: string
  description: string
  serverCount?: number
  toolCount?: number
  /** 后端列表接口不返回这两个时间戳，故标可选。 */
  createdAt?: number
  updatedAt?: number
}
