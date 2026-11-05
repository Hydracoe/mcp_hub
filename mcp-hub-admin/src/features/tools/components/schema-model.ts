/** inputSchema → 表单模型的纯逻辑。只认 string/number/boolean/enum 四类叶子，
 *  数组/对象/嵌套一律判为不支持，由调用方退回原始 JSON（解释错比手写 JSON 更糟）。 */

export type FieldKind = 'string' | 'number' | 'boolean' | 'enum' | 'json'

export interface SchemaField {
  name: string
  kind: FieldKind
  required: boolean
  description?: string
  enumValues?: string[]
  placeholder?: string
}

export interface ParsedSchema {
  fields: SchemaField[]
  /** false 表示结构超出上面的覆盖面，调用方应退回 JSON 模式。 */
  supported: boolean
}

const LEAF = new Set(['string', 'number', 'integer', 'boolean'])

export function parseSchema(schema: Record<string, unknown> | undefined): ParsedSchema {
  if (!schema || typeof schema !== 'object') return { fields: [], supported: false }
  const props = schema.properties
  if (!props || typeof props !== 'object') {
    // 无参数工具是合法的：没有 properties 就等于调用时不需要参数。
    const type = schema.type
    if (type === 'object' || type === undefined) return { fields: [], supported: true }
    return { fields: [], supported: false }
  }

  const requiredList = Array.isArray(schema.required) ? schema.required.map(String) : []
  const fields: SchemaField[] = []
  let supported = true

  for (const [name, raw] of Object.entries(props as Record<string, unknown>)) {
    if (!raw || typeof raw !== 'object') {
      supported = false
      continue
    }
    const def = raw as Record<string, unknown>
    const type = typeof def.type === 'string' ? def.type : undefined
    const enumValues = Array.isArray(def.enum) ? def.enum.map(String) : undefined
    const description = typeof def.description === 'string' ? def.description : undefined
    const required = requiredList.includes(name)

    if (enumValues) {
      fields.push({ name, kind: 'enum', required, description, enumValues })
    } else if (type && LEAF.has(type)) {
      const kind: FieldKind = type === 'boolean' ? 'boolean' : type === 'string' ? 'string' : 'number'
      const placeholder =
        typeof def.default !== 'undefined'
          ? `默认 ${String(def.default)}`
          : def.maximum !== undefined
            ? `最大 ${String(def.maximum)}`
            : undefined
      fields.push({ name, kind, required, description, placeholder })
    } else {
      supported = false
    }
  }

  return { fields, supported }
}
