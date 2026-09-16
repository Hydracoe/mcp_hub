/**
 * 分类英文名（slug）的归一化规则。
 *
 * 与后端 `store.NormalizeCategorySlug` 是同一套规则，必须保持一致：
 * 两边不一致的症状是「表单里预览的名字」与「实际工具名」对不上，
 * 而这类差异只会被 AI 客户端在调用时以「工具不存在」的形式暴露出来。
 *
 * 规则：小写化；`-` `_` 空格 `.` `/` `\` 折成 `-`；其余非 [a-z0-9]
 * 的字符直接丢弃；压缩连续 `-`；去掉首尾 `-`；截断到 32 个字符。
 *
 * 中文字符是「丢弃」而不是「折成 `-`」：折的话「天气 weather」会变成
 * `-weather`，看起来像用户自己多打了个横杠。
 */

/** 英文名长度上限。上限的存在理由是工具名的可读性，不是存储。 */
export const MAX_CATEGORY_SLUG_LEN = 32

const DASH_LIKE = new Set(['-', '_', ' ', '.', '/', '\\'])

export function normalizeCategorySlug(raw: string): string {
  let out = ''
  for (const ch of (raw ?? '').trim()) {
    if ((ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9')) {
      out += ch
    } else if (ch >= 'A' && ch <= 'Z') {
      out += ch.toLowerCase()
    } else if (DASH_LIKE.has(ch)) {
      // 开头的横杠与连续横杠都跳过——这里就地压缩，
      // 免得后面还要再扫一遍。
      if (out !== '' && !out.endsWith('-')) out += '-'
    }
  }
  out = out.replace(/-+$/, '')
  return out.length > MAX_CATEGORY_SLUG_LEN ? out.slice(0, MAX_CATEGORY_SLUG_LEN).replace(/-+$/, '') : out
}

/**
 * 预览对外工具名的前缀段。
 *
 * 英文名为空时回退 `uncategorized`：与后端 `registry.CategorySlug` 一致——
 * 分类段可以不完美，但不能缺席。
 */
export function slugPrefixOrFallback(slug: string): string {
  return normalizeCategorySlug(slug) || 'uncategorized'
}

/** 预览某个分类下工具名的形态：`<分类英文名>__<serverKey>__<工具名>`。 */
export function previewToolName(slug: string, serverKey = 'github', toolName = 'create_issue'): string {
  return `${slugPrefixOrFallback(slug)}__${serverKey}__${toolName}`
}
