import { describe, it, expect } from 'vitest'
import {
  decodeFilters,
  defineFilterSchema,
  withField,
  withToggle,
  withCleared,
  isFilterActive,
} from './filter-state-core'

const schema = defineFilterSchema({
  ns: { param: 'namespace', type: 'set' },
  sev: { param: 'severity', type: 'set' },
  q: { param: 'q', type: 'text' },
  group: { param: 'group', type: 'single', default: 'none' },
  managed: { param: 'managed', type: 'boolean' },
})

const p = (s = '') => new URLSearchParams(s)

describe('filter-state core', () => {
  it('decodes params by type', () => {
    const v = decodeFilters(schema, p('namespace=argocd,default&severity=high&q=foo&managed=1'))
    expect([...v.ns]).toEqual(['argocd', 'default'])
    expect([...v.sev]).toEqual(['high'])
    expect(v.q).toBe('foo')
    expect(v.managed).toBe(true)
  })

  it('applies string defaults when the param is absent', () => {
    const v = decodeFilters(schema, p(''))
    expect(v.group).toBe('none')
    expect(v.q).toBe('')
    expect(v.managed).toBe(false)
    expect(v.ns.size).toBe(0)
  })

  it('toggles a set member and omits the param when empty (empty = all)', () => {
    const added = withToggle(schema, p(''), 'ns', 'argocd')
    expect(added.toString()).toBe('namespace=argocd')
    const removed = withToggle(schema, added, 'ns', 'argocd')
    expect(removed.toString()).toBe('')
  })

  it('composes successive toggles (each sees the previous result)', () => {
    let params = p('')
    params = withToggle(schema, params, 'ns', 'a')
    params = withToggle(schema, params, 'ns', 'b')
    expect([...decodeFilters(schema, params).ns].sort()).toEqual(['a', 'b'])
  })

  it('withField replaces a set wholesale and clears when empty', () => {
    const set = withField(schema, p('namespace=x'), 'ns', new Set(['a', 'b']))
    expect([...decodeFilters(schema, set).ns]).toEqual(['a', 'b'])
    expect(withField(schema, set, 'ns', new Set<string>()).toString()).toBe('')
  })

  it('omits a string param when equal to its default', () => {
    expect(withField(schema, p(''), 'group', 'namespace').toString()).toBe('group=namespace')
    expect(withField(schema, p('group=namespace'), 'group', 'none').toString()).toBe('')
  })

  it('encodes a boolean as =1 and omits false', () => {
    expect(withField(schema, p(''), 'managed', true).toString()).toBe('managed=1')
    expect(withField(schema, p('managed=1'), 'managed', false).toString()).toBe('')
  })

  it('clears one field or all schema fields, leaving unrelated params', () => {
    const one = withCleared(schema, p('namespace=a&severity=high&keep=yes'), ['ns'])
    const oneV = decodeFilters(schema, one)
    expect(oneV.ns.size).toBe(0)
    expect(oneV.sev.size).toBe(1)
    expect(one.get('keep')).toBe('yes')
    expect(withCleared(schema, p('namespace=a&severity=high&keep=yes')).toString()).toBe('keep=yes')
  })

  it('isFilterActive is true only when a field diverges from its default', () => {
    expect(isFilterActive(schema, decodeFilters(schema, p('q=foo')))).toBe(true)
    expect(isFilterActive(schema, decodeFilters(schema, p('group=none')))).toBe(false)
    expect(isFilterActive(schema, decodeFilters(schema, p('')))).toBe(false)
  })

  it('writes set values sorted, so the URL is canonical regardless of order', () => {
    const a = withToggle(schema, withToggle(schema, p(''), 'ns', 'zebra'), 'ns', 'alpha')
    expect(a.get('namespace')).toBe('alpha,zebra')
  })

  it('decodes an empty string param (?group=) as the default', () => {
    expect(decodeFilters(schema, p('group=')).group).toBe('none')
    expect(decodeFilters(schema, p('q=')).q).toBe('')
  })

  it('defineFilterSchema rejects duplicate URL params', () => {
    expect(() =>
      defineFilterSchema({ a: { param: 'x', type: 'set' }, b: { param: 'x', type: 'set' } }),
    ).toThrow(/duplicate/)
  })
})
