import { describe, it, expect } from 'vitest'
import {
  parseColumnFilters,
  serializeColumnFilters,
  parseColumnFilterInverts,
  serializeColumnFilterInverts,
  reconcileColumnFilterInverts,
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
})

describe('column filter invert serialization round-trip', () => {
  it('round-trips inverted built-in keys', () => {
    const inverts = { status: true, namespace: true }
    expect(parseColumnFilterInverts(serializeColumnFilterInverts(inverts))).toEqual(inverts)
  })

  it('drops false entries', () => {
    expect(serializeColumnFilterInverts({ status: true, namespace: false })).toBe('status')
  })

  it('encodes custom-column keys whose comma would collide with the delimiter', () => {
    const inverts = { 'label:tier,zone': true }
    const serialized = serializeColumnFilterInverts(inverts)
    expect(serialized).toBe('label%3Atier%2Czone')
    expect(parseColumnFilterInverts(serialized)).toEqual(inverts)
  })

  it('returns an empty object for empty/absent params', () => {
    expect(parseColumnFilterInverts('')).toEqual({})
    expect(parseColumnFilterInverts(null)).toEqual({})
    expect(serializeColumnFilterInverts({})).toBe('')
  })
})

describe('reconcileColumnFilterInverts', () => {
  it('keeps invert flags for columns that have selected values', () => {
    const inverts = { status: true, namespace: true }
    const filters = { status: ['Running'], namespace: ['default'] }
    expect(reconcileColumnFilterInverts(inverts, filters)).toEqual(inverts)
  })

  it('drops orphan flags for columns with no selected values', () => {
    const inverts = { status: true, namespace: true }
    const filters = { status: ['Running'] }
    expect(reconcileColumnFilterInverts(inverts, filters)).toEqual({ status: true })
  })

  it('drops a flag whose column has an empty values array', () => {
    expect(reconcileColumnFilterInverts({ status: true }, { status: [] })).toEqual({})
  })

  it('returns an empty object when there are no filters', () => {
    expect(reconcileColumnFilterInverts({ status: true }, {})).toEqual({})
  })
})
