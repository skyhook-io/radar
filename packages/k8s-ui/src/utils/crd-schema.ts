export interface CRDSchema {
  type?: string
  description?: string
  properties?: Record<string, CRDSchema>
  required?: string[]
  items?: CRDSchema
  additionalProperties?: CRDSchema | boolean
  'x-kubernetes-int-or-string'?: boolean
  'x-kubernetes-preserve-unknown-fields'?: boolean
  'x-kubernetes-validations'?: { rule: string; message?: string }[]
  oneOf?: CRDSchema[]
  anyOf?: CRDSchema[]
  allOf?: CRDSchema[]
  not?: CRDSchema
  [key: string]: unknown
}

export interface CRDVersion {
  name: string
  served?: boolean
  storage?: boolean
  deprecated?: boolean
  deprecationWarning?: string
  schema?: { openAPIV3Schema?: CRDSchema }
}

export type SchemaPath = { kind: 'property' | 'items' | 'map'; name: string }[]
export interface SchemaNode {
  path: SchemaPath
  schema: CRDSchema
  required: boolean
}

export function compareKubeAwareVersions(a: string, b: string): number {
  const parse = (v: string) => /^v(\d+)(?:(alpha|beta)(\d+))?$/.exec(v)
  const x = parse(a), y = parse(b)
  if (!x || !y) return x ? -1 : y ? 1 : a.localeCompare(b)
  const rank = (stage?: string) => stage === 'alpha' ? 0 : stage === 'beta' ? 1 : 2
  return rank(y[2]) - rank(x[2]) || Number(y[1]) - Number(x[1]) || Number(y[3] || 0) - Number(x[3] || 0)
}

export function preferredCRDVersion(versions: CRDVersion[]): CRDVersion | undefined {
  return [...versions].filter(v => v.served).sort((a, b) => compareKubeAwareVersions(a.name, b.name))[0] ?? versions[0]
}

export function schemaTypeLabel(schema: CRDSchema): string {
  if (schema['x-kubernetes-int-or-string']) return 'int-or-string'
  if (schema.type === 'array') return `[]${schema.items ? schemaTypeLabel(schema.items) : 'any'}`
  if (schema.additionalProperties && typeof schema.additionalProperties === 'object') return `map[string]${schemaTypeLabel(schema.additionalProperties)}`
  if (schema.additionalProperties === true) return 'map[string]any'
  return schema.type || 'any'
}

// Array items and map values get no row of their own, as in `kubectl explain`:
// a field's children are the fields of what it holds, reached through a `[]` or
// `{}` path segment.
export function schemaChildren(schema: CRDSchema, path: SchemaPath = []): SchemaNode[] {
  const children: SchemaNode[] = Object.entries(schema.properties || {}).map(([name, child]) => ({
    path: [...path, { kind: 'property', name }], schema: child, required: !!schema.required?.includes(name),
  }))
  if (schema.items) children.push(...schemaChildren(schema.items, [...path, { kind: 'items', name: '[]' }]))
  if (schema.additionalProperties && typeof schema.additionalProperties === 'object') {
    children.push(...schemaChildren(schema.additionalProperties, [...path, { kind: 'map', name: '{}' }]))
  }
  return children
}

/** The schema an array or map holds, whose constraints belong to the field. */
export function schemaElement(schema: CRDSchema): { label: 'items' | 'values'; schema: CRDSchema } | undefined {
  if (schema.items) return { label: 'items', schema: schema.items }
  if (schema.additionalProperties && typeof schema.additionalProperties === 'object') return { label: 'values', schema: schema.additionalProperties }
  return undefined
}

export function schemaLeafName(path: SchemaPath): string {
  for (let i = path.length - 1; i >= 0; i--) if (path[i].kind === 'property') return path[i].name
  return ''
}

/** Display path of everything above the leaf property, with its trailing dot. */
export function schemaParentPath(path: SchemaPath): string {
  let leaf = path.length - 1
  while (leaf >= 0 && path[leaf].kind !== 'property') leaf--
  const parent = displaySchemaPath(path.slice(0, leaf))
  return parent ? `${parent}.` : ''
}

export function displaySchemaPath(path: SchemaPath): string {
  return path.map((segment, i) => segment.kind === 'property' ? `${i ? '.' : ''}${segment.name}` : segment.name).join('')
}

export function schemaConstraints(schema: CRDSchema): { label: string; value: string }[] {
  return ['format', 'enum', 'default', 'minimum', 'maximum', 'exclusiveMinimum', 'exclusiveMaximum', 'multipleOf',
    'minLength', 'maxLength', 'pattern', 'minItems', 'maxItems', 'uniqueItems', 'minProperties', 'maxProperties', 'nullable']
    .filter(key => schema[key] !== undefined)
    .map(key => ({ label: key, value: key === 'enum' && Array.isArray(schema.enum) ? schema.enum.map(v => typeof v === 'string' ? v : JSON.stringify(v)).join(' · ')
      : typeof schema[key] === 'string' ? schema[key] as string : JSON.stringify(schema[key]) }))
}

export function schemaCompositions(schema: CRDSchema): { label: string; value: string }[] {
  return (['oneOf', 'anyOf', 'allOf', 'not'] as const).flatMap(key => {
    const value = schema[key]
    if (!value) return []
    const alternatives = Array.isArray(value) ? value : [value]
    const requiredOnly = alternatives.every(v => v.required?.length && Object.keys(v).every(k => k === 'required'))
    return [{ label: key === 'oneOf' && requiredOnly ? 'One of' : key,
      value: requiredOnly ? alternatives.map(v => v.required!.join(' + ')).join(' · ') : JSON.stringify(value) }]
  })
}

export function indexCRDSchema(schema: CRDSchema | undefined): SchemaNode[] {
  if (!schema) return []
  const result: SchemaNode[] = []
  const stack = schemaChildren(schema).reverse()
  while (stack.length) {
    const node = stack.pop()!
    result.push(node)
    stack.push(...schemaChildren(node.schema, node.path).reverse())
  }
  return result
}

export function filterCRDSchema(index: SchemaNode[], query: string, limit = 200): { nodes: SchemaNode[]; total: number } {
  const term = query.toLowerCase()
  const nodes: SchemaNode[] = []
  let total = 0
  for (const node of index) {
    if (!displaySchemaPath(node.path).toLowerCase().includes(term)) continue
    total++
    if (nodes.length < limit) nodes.push(node)
  }
  return { nodes, total }
}
