import { describe, expect, it } from 'vitest'
import { compareKubeAwareVersions, displaySchemaPath, filterCRDSchema, indexCRDSchema, preferredCRDVersion, schemaChildren, schemaCompositions, schemaConstraints, schemaTypeLabel, schemaLeafName, schemaParentPath, schemaElement } from './crd-schema'

describe('CRD schemas', () => {
  it('labels arrays, maps, boolean additionalProperties and Kubernetes extensions', () => {
    expect(schemaTypeLabel({ type: 'array', items: { type: 'string' } })).toBe('[]string')
    expect(schemaTypeLabel({ type: 'object', additionalProperties: { type: 'integer' } })).toBe('map[string]integer')
    expect(schemaTypeLabel({ type: 'object', additionalProperties: true })).toBe('map[string]any')
    expect(schemaTypeLabel({ type: 'object', additionalProperties: false })).toBe('object')
    expect(schemaTypeLabel({ 'x-kubernetes-int-or-string': true })).toBe('int-or-string')
    expect(schemaTypeLabel({ type: 'object', 'x-kubernetes-preserve-unknown-fields': true })).toBe('object')
  })
  it('retains requiredness, array/map traversal and dotted property identity', () => {
    const nodes = indexCRDSchema({ required: ['a.b'], properties: {
      'a.b': { type: 'string' }, a: { properties: { b: { type: 'number' } } },
      list: { items: { properties: { x: { type: 'boolean' } } } },
      map: { additionalProperties: { properties: { y: { type: 'integer' } } } },
    } })
    expect(nodes[0].required).toBe(true)
    const matching = nodes.filter(n => displaySchemaPath(n.path) === 'a.b')
    expect(matching).toHaveLength(2)
    expect(matching[0].path).not.toEqual(matching[1].path)
    expect(nodes.map(n => displaySchemaPath(n.path))).toContain('list[].x')
    expect(nodes.map(n => displaySchemaPath(n.path))).toContain('map{}.y')
    expect(nodes.map(n => displaySchemaPath(n.path))).not.toContain('list[]')
    expect(nodes.map(n => displaySchemaPath(n.path))).not.toContain('map{}')
    const deep = nodes.find(n => displaySchemaPath(n.path) === 'list[].x')!
    expect(schemaLeafName(deep.path)).toBe('x')
    expect(schemaParentPath(deep.path)).toBe('list[].')
    expect(schemaElement({ type: 'array', items: { type: 'string', enum: ['a'] } })).toEqual({ label: 'items', schema: { type: 'string', enum: ['a'] } })
    expect(schemaChildren({ additionalProperties: true })).toEqual([])
  })
  it('retains zero/false/default, constraints, CEL and compositions', () => {
    const schema = { type: 'integer', minimum: 0, maximum: 4, default: false, enum: [0, 1], format: 'int32', pattern: '^a', minLength: 0, maxLength: 4, minItems: 0, maxItems: 2,
      'x-kubernetes-validations': [{ rule: 'self > 0', message: 'positive' }],
      oneOf: [{ required: ['a'] }, { required: ['b', 'c'] }], anyOf: [{ type: 'string' }], allOf: [{ minimum: 0 }], not: { type: 'boolean' } }
    expect(schemaConstraints(schema)).toEqual(expect.arrayContaining([{ label: 'minimum', value: '0' }, { label: 'default', value: 'false' }, { label: 'enum', value: '0 · 1' }]))
    expect(schema['x-kubernetes-validations'][0]).toEqual({ rule: 'self > 0', message: 'positive' })
    expect(schemaCompositions(schema)).toEqual([{ label: 'One of', value: 'a · b + c' }, { label: 'anyOf', value: '[{"type":"string"}]' }, { label: 'allOf', value: '[{"minimum":0}]' }, { label: 'not', value: '{"type":"boolean"}' }])
  })
  it('orders kube versions by stability then descending major/minor, with other names last', () => {
    expect(['v2alpha2', 'foo', 'v1beta2', 'v2', 'v1', 'v3beta1', 'v1beta10', 'bar', 'v2alpha10'].sort(compareKubeAwareVersions)).toEqual(['v2', 'v1', 'v3beta1', 'v1beta10', 'v1beta2', 'v2alpha10', 'v2alpha2', 'bar', 'foo'])
    expect(preferredCRDVersion([{ name: 'v3', served: false }, { name: 'v1', served: true }, { name: 'v2', served: true }])?.name).toBe('v2')
  })
  it('filters case-insensitive displayed paths with a 200 cap and exact total; accepts missing schema', () => {
    const index = indexCRDSchema({ properties: Object.fromEntries(Array.from({ length: 245 }, (_, i) => [`field${i}`, { type: 'string' }])) })
    const result = filterCRDSchema(index, 'FIELD')
    expect(result.total).toBe(245)
    expect(result.nodes).toHaveLength(200)
    expect(indexCRDSchema(undefined)).toEqual([])
    expect(preferredCRDVersion([])).toBeUndefined()
  })
})
