import { describe, it, expect } from 'vitest'
import { parseColumnFilters, serializeColumnFilters } from './resource-utils'

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
})
