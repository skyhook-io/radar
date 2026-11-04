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
  parent?: SchemaNode
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
export function schemaChildren(schema: CRDSchema, path: SchemaPath = [], parent?: SchemaNode): SchemaNode[] {
  const children: SchemaNode[] = Object.entries(schema.properties || {}).map(([name, child]) => ({
    path: [...path, { kind: 'property', name }], schema: child, required: !!schema.required?.includes(name), parent,
  }))
  if (schema.items) children.push(...schemaChildren(schema.items, [...path, { kind: 'items', name: '[]' }], parent))
  if (schema.additionalProperties && typeof schema.additionalProperties === 'object') {
    children.push(...schemaChildren(schema.additionalProperties, [...path, { kind: 'map', name: '{}' }], parent))
  }
  return children
}

export interface EnclosingRules {
  /** Display path of the enclosing field; empty for the document root. */
  path: string
  validations: { rule: string; message?: string }[]
  compositions: { label: string; value: string }[]
}

// A field's own rules, then those of each array item or map value it holds
// (`spec.groups`, `spec.groups[]`, `spec.groups[]{}`), each under the path its
// `self` refers to. Nearest the leaf first.
function rulesOf(schema: CRDSchema, path: string): EnclosingRules[] {
  const levels: EnclosingRules[] = []
  for (let level: CRDSchema | undefined = schema, at = path; level;) {
    const validations = level['x-kubernetes-validations'] ?? []
    const compositions = schemaCompositions(level)
    if (validations.length || compositions.length) levels.push({ path: at, validations, compositions })
    const element = schemaElement(level)
    at += element?.label === 'items' ? '[]' : '{}'
    level = element?.schema
  }
  return levels.reverse()
}

/** Rules declared on the fields that contain `node`, nearest first. CEL rules
 *  and oneOf/anyOf constraints on a parent often decide whether a field's value
 *  is accepted, so a field found by search has to show them too. */
export function enclosingRules(node: SchemaNode, root: CRDSchema): EnclosingRules[] {
  const result: EnclosingRules[] = []
  for (let ancestor = node.parent; ancestor; ancestor = ancestor.parent) {
    result.push(...rulesOf(ancestor.schema, displaySchemaPath(ancestor.path)))
  }
  result.push(...rulesOf(root, ''))
  return result
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

// A property name that would read as path syntax is quoted, so a literal
// `a.b` key and a nested `a` → `b` never display alike.
function displayPropertyName(name: string): string {
  return /^[A-Za-z0-9_$@-]+$/.test(name) ? name : `[${JSON.stringify(name)}]`
}

export function displaySchemaPath(path: SchemaPath): string {
  return path.map((segment, i) => {
    if (segment.kind !== 'property') return segment.name
    const name = displayPropertyName(segment.name)
    return name.startsWith('[') || i === 0 ? name : `.${name}`
  }).join('')
}

const EXTENSION_FACTS: [key: string, label: string][] = [
  ['x-kubernetes-list-type', 'list type'],
  ['x-kubernetes-list-map-keys', 'list keys'],
  ['x-kubernetes-map-type', 'map type'],
]

export function schemaConstraints(schema: CRDSchema): { label: string; value: string }[] {
  const extensions = EXTENSION_FACTS
    .filter(([key]) => schema[key] !== undefined)
    .map(([key, label]) => ({ label, value: Array.isArray(schema[key]) ? (schema[key] as unknown[]).join(', ') : String(schema[key]) }))
  return [...extensions, ...['format', 'enum', 'default', 'minimum', 'maximum', 'exclusiveMinimum', 'exclusiveMaximum', 'multipleOf',
    'minLength', 'maxLength', 'pattern', 'minItems', 'maxItems', 'uniqueItems', 'minProperties', 'maxProperties', 'nullable']
    .filter(key => schema[key] !== undefined)
    .map(key => ({ label: key, value: key === 'enum' && Array.isArray(schema.enum) ? schema.enum.map(v => typeof v === 'string' ? v : JSON.stringify(v)).join(' · ')
      : typeof schema[key] === 'string' ? schema[key] as string : JSON.stringify(schema[key]) }))]
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
    stack.push(...schemaChildren(node.schema, node.path, node).reverse())
  }
  return result
}

// Paths in API server errors and kubectl output index into lists and maps
// (`spec.ports[0].name`, `metadata.labels[app]`), and people often leave the
// brackets out entirely. Matching ignores both so a pasted path still finds its field.
function matchablePath(path: string): string {
  return path.toLowerCase().replace(/\[(?!")[^\]]*\]|\{\}/g, '').replace(/\.{2,}/g, '.')
}

export function filterCRDSchema(index: SchemaNode[], query: string, limit = 200): { nodes: SchemaNode[]; total: number } {
  const term = query.trim().toLowerCase()
  const looseTerm = matchablePath(term)
  // The field the query names exactly leads, so the cap can never hide it.
  const exact: SchemaNode[] = []
  const nodes: SchemaNode[] = []
  let total = 0
  for (const node of index) {
    const display = displaySchemaPath(node.path).toLowerCase()
    const loose = matchablePath(display)
    if (!display.includes(term) && !(looseTerm && loose.includes(looseTerm))) continue
    total++
    if (display === term || loose === looseTerm) exact.push(node)
    else if (nodes.length < limit) nodes.push(node)
  }
  return { nodes: [...exact, ...nodes].slice(0, limit), total }
}
