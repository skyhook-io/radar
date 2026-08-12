import { describe, expect, it } from 'vitest'
import { resolveDefaultSort } from './ResourcesView'

describe('resolveDefaultSort', () => {
  it('returns no sort when there is no preference', () => {
    expect(resolveDefaultSort(null, 'pods')).toEqual({ column: null, direction: null })
    expect(resolveDefaultSort(undefined, 'pods')).toEqual({ column: null, direction: null })
  })

  it('applies a column the kind has', () => {
    expect(resolveDefaultSort({ column: 'age', direction: 'desc' }, 'pods')).toEqual({
      column: 'age',
      direction: 'desc',
    })
    expect(resolveDefaultSort({ column: 'restarts', direction: 'asc' }, 'pods')).toEqual({
      column: 'restarts',
      direction: 'asc',
    })
  })

  it('falls back to the kind default when the column is absent from that table', () => {
    // ConfigMaps have no Status or Restarts column; Nodes are cluster-scoped and
    // have no Namespace column. Sorting by an absent column would produce an
    // arbitrary order with no header arrow to undo it from.
    expect(resolveDefaultSort({ column: 'status', direction: 'asc' }, 'configmaps')).toEqual({
      column: null,
      direction: null,
    })
    expect(resolveDefaultSort({ column: 'restarts', direction: 'desc' }, 'configmaps')).toEqual({
      column: null,
      direction: null,
    })
    expect(resolveDefaultSort({ column: 'namespace', direction: 'asc' }, 'nodes')).toEqual({
      column: null,
      direction: null,
    })
  })

  it('resolves the kind through its plural/group mapping', () => {
    expect(resolveDefaultSort({ column: 'age', direction: 'asc' }, 'Pod')).toEqual({
      column: 'age',
      direction: 'asc',
    })
  })

  it('leaves unknown kinds on the shared default column set', () => {
    // Unknown kinds render DEFAULT_COLUMNS (name/namespace/status/age).
    expect(resolveDefaultSort({ column: 'status', direction: 'asc' }, 'widgets', 'example.com')).toEqual({
      column: 'status',
      direction: 'asc',
    })
    expect(resolveDefaultSort({ column: 'restarts', direction: 'asc' }, 'widgets', 'example.com')).toEqual({
      column: null,
      direction: null,
    })
  })
})
