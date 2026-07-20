import { describe, it, expect } from 'vitest'
import {
  parseColumnFilters,
  serializeColumnFilters,
  parseColumnFilterExcludes,
} from './resource-utils'

describe('column filter serialization round-trip', () => {
  it('round-trips built-in keys', () => {
    const filters = { status: ['Running'], namespace: ['kube-system', 'default'] }
    expect(parseColumnFilters(serializeColumnFilters(filters))).toEqual(filters)
  })

  it('round-trips custom-column keys whose own colon collides with the delimiter', () => {
    const filters = { 'label:tier': ['control-plane'], 'annotation:foo/bar': ['x'] }
    const serialized = serializeColumnFilters(filters)
    // The key colon must be encoded so the first literal ':' is the delimiter.
    expect(serialized).toBe('label%3Atier:control-plane|annotation%3Afoo%2Fbar:x')
    expect(parseColumnFilters(serialized)).toEqual(filters)
  })

  it('preserves commas inside values', () => {
    const filters = { conditions: ['Ready,SchedulingDisabled'] }
    expect(parseColumnFilters(serializeColumnFilters(filters))).toEqual(filters)
  })

  it('parses legacy unencoded built-in keys', () => {
    expect(parseColumnFilters('status:Running')).toEqual({ status: ['Running'] })
  })

  it('treats a two-part filter as implicit include (no excludes)', () => {
    expect(parseColumnFilterExcludes('status:Running')).toEqual({})
  })
})

describe('column filter include/exclude operator', () => {
  it('serializes excluded columns with the explicit exclude operator', () => {
    const filters = { status: ['Running', 'Completed'], namespace: ['default'] }
    const excludes = { status: true }
    expect(serializeColumnFilters(filters, excludes)).toBe(
      'status:exclude:Running,Completed|namespace:default'
    )
  })

  it('round-trips values through an excluded column', () => {
    const filters = { status: ['Running', 'Completed'] }
    const serialized = serializeColumnFilters(filters, { status: true })
    expect(parseColumnFilters(serialized)).toEqual(filters)
    expect(parseColumnFilterExcludes(serialized)).toEqual({ status: true })
  })

  it('parses the explicit include operator as non-excluded', () => {
    expect(parseColumnFilters('namespace:include:default')).toEqual({ namespace: ['default'] })
    expect(parseColumnFilterExcludes('namespace:include:default')).toEqual({})
  })

  it('keeps a value literally named "exclude" in the two-part form', () => {
    expect(parseColumnFilters('reason:exclude')).toEqual({ reason: ['exclude'] })
    expect(parseColumnFilterExcludes('reason:exclude')).toEqual({})
  })

  it('handles a value literally named "exclude" under the exclude operator', () => {
    const serialized = serializeColumnFilters({ reason: ['exclude'] }, { reason: true })
    expect(serialized).toBe('reason:exclude:exclude')
    expect(parseColumnFilters(serialized)).toEqual({ reason: ['exclude'] })
    expect(parseColumnFilterExcludes(serialized)).toEqual({ reason: true })
  })

  it('does not emit an operator for an excluded column with no values', () => {
    expect(serializeColumnFilters({ status: [] }, { status: true })).toBe('')
    expect(parseColumnFilterExcludes('')).toEqual({})
  })

  it('preserves the exclude operator for custom-column keys', () => {
    const filters = { 'label:tier': ['control-plane'] }
    const serialized = serializeColumnFilters(filters, { 'label:tier': true })
    expect(serialized).toBe('label%3Atier:exclude:control-plane')
    expect(parseColumnFilters(serialized)).toEqual(filters)
    expect(parseColumnFilterExcludes(serialized)).toEqual({ 'label:tier': true })
  })

  it('returns empty structures for empty/absent params', () => {
    expect(parseColumnFilters('')).toEqual({})
    expect(parseColumnFilters(null)).toEqual({})
    expect(parseColumnFilterExcludes(null)).toEqual({})
    expect(serializeColumnFilters({})).toBe('')
  })

  it('ignores prototype-polluting keys from a crafted param', () => {
    const parsed = parseColumnFilters('__proto__:Running|constructor:x|status:Running')
    expect(parsed).toEqual({ status: ['Running'] })
    expect(Object.prototype.hasOwnProperty.call(parsed, '__proto__')).toBe(false)
    expect(({} as Record<string, unknown>).polluted).toBeUndefined()
    expect(parseColumnFilterExcludes('__proto__:exclude:Running')).toEqual({})
  })

  it('lets the last segment win the mode when a column repeats', () => {
    expect(parseColumnFilters('status:exclude:Running|status:Completed')).toEqual({ status: ['Completed'] })
    expect(parseColumnFilterExcludes('status:exclude:Running|status:Completed')).toEqual({})

    expect(parseColumnFilters('status:Running|status:exclude:Completed')).toEqual({ status: ['Completed'] })
    expect(parseColumnFilterExcludes('status:Running|status:exclude:Completed')).toEqual({ status: true })
  })
})
