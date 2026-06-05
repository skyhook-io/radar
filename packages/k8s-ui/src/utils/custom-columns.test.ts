import { describe, it, expect } from 'vitest'
import { customColumnKey, readCustomColumnValue, sanitizeCustomColumnDefs } from './custom-columns'

describe('customColumnKey', () => {
  it('encodes source and path', () => {
    expect(customColumnKey({ source: 'label', path: 'topology.kubernetes.io/zone' })).toBe('label:topology.kubernetes.io/zone')
    expect(customColumnKey({ source: 'annotation', path: 'foo/bar' })).toBe('annotation:foo/bar')
  })

  it('produces equal keys for same source+path (dedupe contract)', () => {
    const a = customColumnKey({ source: 'label', path: 'x' })
    const b = customColumnKey({ source: 'label', path: 'x' })
    expect(a).toBe(b)
  })

  it('distinguishes source for the same path', () => {
    expect(customColumnKey({ source: 'label', path: 'x' }))
      .not.toBe(customColumnKey({ source: 'annotation', path: 'x' }))
  })
})

describe('readCustomColumnValue', () => {
  const res = (meta: any) => ({ metadata: meta })

  it('reads a label value', () => {
    expect(readCustomColumnValue(res({ labels: { zone: 'us-east-1a' } }), { source: 'label', path: 'zone' })).toBe('us-east-1a')
  })

  it('reads an annotation value', () => {
    expect(readCustomColumnValue(res({ annotations: { team: 'infra' } }), { source: 'annotation', path: 'team' })).toBe('infra')
  })

  it('does not cross labels and annotations', () => {
    const r = res({ labels: { k: 'fromLabel' }, annotations: { k: 'fromAnnotation' } })
    expect(readCustomColumnValue(r, { source: 'label', path: 'k' })).toBe('fromLabel')
    expect(readCustomColumnValue(r, { source: 'annotation', path: 'k' })).toBe('fromAnnotation')
  })

  it('returns empty string for a missing key', () => {
    expect(readCustomColumnValue(res({ labels: { a: '1' } }), { source: 'label', path: 'b' })).toBe('')
  })

  it('returns empty string when metadata/bag is absent', () => {
    expect(readCustomColumnValue({}, { source: 'label', path: 'b' })).toBe('')
    expect(readCustomColumnValue(res({}), { source: 'annotation', path: 'b' })).toBe('')
    expect(readCustomColumnValue(undefined, { source: 'label', path: 'b' })).toBe('')
  })

  it('coerces non-string values, but maps null/undefined to empty (not the literal "null")', () => {
    expect(readCustomColumnValue(res({ labels: { n: 5 as any } }), { source: 'label', path: 'n' })).toBe('5')
    expect(readCustomColumnValue(res({ labels: { n: null as any } }), { source: 'label', path: 'n' })).toBe('')
  })
})

describe('sanitizeCustomColumnDefs', () => {
  it('returns [] for non-array input', () => {
    expect(sanitizeCustomColumnDefs(undefined)).toEqual([])
    expect(sanitizeCustomColumnDefs(null)).toEqual([])
    expect(sanitizeCustomColumnDefs({ source: 'label', path: 'x' })).toEqual([])
    expect(sanitizeCustomColumnDefs('label:x')).toEqual([])
  })

  it('keeps well-formed defs', () => {
    const defs = [
      { source: 'label', path: 'zone' },
      { source: 'annotation', path: 'team' },
    ]
    expect(sanitizeCustomColumnDefs(defs)).toEqual(defs)
  })

  it('drops entries with invalid source, missing or blank path', () => {
    const raw = [
      { source: 'label', path: 'good' },
      { source: 'lable', path: 'typo-source' },
      { source: 'label', path: '' },
      { source: 'label', path: '   ' },
      { source: 'annotation' },
      null,
      'label:x',
    ]
    expect(sanitizeCustomColumnDefs(raw)).toEqual([{ source: 'label', path: 'good' }])
  })

  it('drops a non-string path without throwing (predicate short-circuits before trim)', () => {
    const raw = [
      { source: 'label', path: 42 },
      { source: 'label', path: { nested: true } },
      { source: 'annotation', path: 'ok' },
    ]
    expect(() => sanitizeCustomColumnDefs(raw)).not.toThrow()
    expect(sanitizeCustomColumnDefs(raw)).toEqual([{ source: 'annotation', path: 'ok' }])
  })

  it('trims paths so the load path matches the add path', () => {
    expect(sanitizeCustomColumnDefs([{ source: 'label', path: '  zone  ' }]))
      .toEqual([{ source: 'label', path: 'zone' }])
  })

  it('dedupes by key, keeping the first occurrence', () => {
    const raw = [
      { source: 'label', path: 'zone' },
      { source: 'label', path: 'zone' },
      { source: 'label', path: ' zone ' },
      { source: 'annotation', path: 'zone' },
    ]
    expect(sanitizeCustomColumnDefs(raw)).toEqual([
      { source: 'label', path: 'zone' },
      { source: 'annotation', path: 'zone' },
    ])
  })
})
