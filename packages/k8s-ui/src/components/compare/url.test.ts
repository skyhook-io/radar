import { describe, it, expect } from 'vitest'
import { parseRef, refToParam } from './url'

describe('parseRef', () => {
  it('parses namespaced ref into namespace and name', () => {
    expect(parseRef('prod/api')).toEqual({ namespace: 'prod', name: 'api' })
  })

  it('treats slashless value as cluster-scoped', () => {
    expect(parseRef('my-cluster-role')).toEqual({ namespace: '', name: 'my-cluster-role' })
  })

  it('returns null for null', () => {
    expect(parseRef(null)).toBeNull()
  })

  it('returns null for undefined', () => {
    expect(parseRef(undefined)).toBeNull()
  })

  it('returns null for empty string', () => {
    expect(parseRef('')).toBeNull()
  })

  it('returns null when name is empty after the slash', () => {
    // A URL like `?a=prod/` would otherwise leave the caller stuck in loading
    // because the gated query has nothing to fetch.
    expect(parseRef('prod/')).toBeNull()
    expect(parseRef('/')).toBeNull()
  })

  it('splits on FIRST slash only — pins behavior against name shape changes', () => {
    expect(parseRef('ns/a/b')).toEqual({ namespace: 'ns', name: 'a/b' })
  })

  it('handles namespace with leading hyphen edge case', () => {
    expect(parseRef('-ns/api')).toEqual({ namespace: '-ns', name: 'api' })
  })
})

describe('refToParam', () => {
  it('emits ns/name for namespaced', () => {
    expect(refToParam({ namespace: 'prod', name: 'api' })).toBe('prod/api')
  })

  it('emits bare name for cluster-scoped', () => {
    expect(refToParam({ namespace: '', name: 'my-cluster-role' })).toBe('my-cluster-role')
  })
})

describe('parseRef + refToParam round-trip', () => {
  it('preserves namespaced refs', () => {
    const input = 'prod/api'
    expect(refToParam(parseRef(input)!)).toBe(input)
  })

  it('preserves cluster-scoped refs', () => {
    const input = 'my-cluster-role'
    expect(refToParam(parseRef(input)!)).toBe(input)
  })
})
