/** 列表页纯计算工具，只做数组变换，与 `hooks/use-list-query-state` 分开（那边管 URL 与状态）。 */

/** 本地内存版的分页（数据已全量在前端时用）。页码越界会被夹到最后一页。 */
export function paginate<T>(rows: T[], page: number, pageSize: number) {
  const pages = Math.max(1, Math.ceil(rows.length / pageSize))
  const current = Math.min(page, pages)
  return {
    rows: rows.slice((current - 1) * pageSize, current * pageSize),
    page: current,
    pages,
    total: rows.length,
  }
}

/** 稳定的本地排序：数字按数值比，其余按中文排序规则比字符串。 */
export function sortRows<T>(rows: T[], key: keyof T | ((row: T) => unknown), dir: 1 | -1): T[] {
  const get = typeof key === 'function' ? key : (row: T) => row[key]
  return [...rows].sort((a, b) => {
    const va = get(a)
    const vb = get(b)
    if (typeof va === 'number' && typeof vb === 'number') return (va - vb) * dir
    const sa = String(va ?? '')
    const sb = String(vb ?? '')
    return sa.localeCompare(sb, 'zh-Hans-CN') * dir
  })
}
