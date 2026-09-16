/**
 * 分类英文名（slug）归一化。规则必须与后端 `store.NormalizeCategorySlug`
 * 一致，否则表单里显示的归一化结果和实际存储对不上。
 *
 * 小写化；`- _ 空格 . / \` 折成 `-`；其余非 [a-z0-9] 丢弃；压缩连续 `-`；
 * 去掉首尾 `-`；截断到 32 字符。中文是「丢弃」不是「折成 -」——
 * 折的话「天气 weather」会变成 `-weather`，像用户自己多打了个横杠。
 */

/** 长度上限，为的是工具名可读，不是存储限制。 */
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
      // 开头与连续的横杠就地跳掉，免得后面再扫一遍。
      if (out !== '' && !out.endsWith('-')) out += '-'
    }
  }
  out = out.replace(/-+$/, '')
  return out.length > MAX_CATEGORY_SLUG_LEN ? out.slice(0, MAX_CATEGORY_SLUG_LEN).replace(/-+$/, '') : out
}
