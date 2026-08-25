import { describe, expect, it } from 'vitest'
import { CROSS_KIND_SORT_COLUMNS, isSortableColumn, resolveDefaultSort, sortColumnLabel } from './ResourcesView'

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

describe('resolveDefaultSort with columns outside KNOWN_COLUMNS', () => {
  it('applies a user-defined label column', () => {
    expect(resolveDefaultSort({ column: 'label:app', direction: 'asc' }, 'pods', '', ['label:app'])).toEqual({
      column: 'label:app',
      direction: 'asc',
    })
  })

  it('applies a host-injected leading column', () => {
    expect(resolveDefaultSort({ column: 'cluster', direction: 'desc' }, 'pods', '', ['cluster'])).toEqual({
      column: 'cluster',
      direction: 'desc',
    })
  })

  it('still falls back for a kind that does not carry that extra column', () => {
    expect(resolveDefaultSort({ column: 'label:app', direction: 'asc' }, 'pods', '', [])).toEqual({
      column: null,
      direction: null,
    })
  })
})

describe('sortability, not just existence', () => {
  it('rejects a column that exists on the kind but cannot be sorted', () => {
    // Pods render podIP and node; getSortValue has no ordering for either, and
    // their headers show no sort affordance — so the preference must not apply.
    expect(isSortableColumn('podIP', 'pods')).toBe(false)
    expect(resolveDefaultSort({ column: 'podIP', direction: 'asc' }, 'pods')).toEqual({
      column: null,
      direction: null,
    })
  })

  it('rejects a host extra column that ships no getSortValue', () => {
    expect(isSortableColumn('cluster', 'pods', '', [])).toBe(false)
  })

  it('accepts sortable built-ins present on the kind', () => {
    expect(isSortableColumn('restarts', 'pods')).toBe(true)
    expect(isSortableColumn('age', 'configmaps')).toBe(true)
  })

  it('rejects a sortable built-in that the kind does not render', () => {
    expect(isSortableColumn('restarts', 'configmaps')).toBe(false)
    expect(isSortableColumn('namespace', 'nodes')).toBe(false)
  })
})

describe('sort column labels', () => {
  it('matches the header for keys that de-camel-casing gets wrong', () => {
    // The table header renders "CPU" and "Up-to-date"; deriving the label from
    // the key would show "Cpu" and "Up To Date" in Settings for the same column.
    expect(sortColumnLabel('cpu')).toBe('CPU')
    expect(sortColumnLabel('upToDate')).toBe('Up-to-date')
  })

  it('names every sortable built-in', () => {
    for (const key of ['name', 'namespace', 'age', 'status', 'restarts', 'memory', 'lastSeen']) {
      expect(sortColumnLabel(key)).toBeTruthy()
    }
  })

  it('returns undefined for columns it cannot name, so callers can fall back', () => {
    expect(sortColumnLabel('label:app')).toBeUndefined()
    expect(sortColumnLabel('cluster')).toBeUndefined()
  })

  it('offers only kind-agnostic columns for the cross-kind setting, all sortable', () => {
    expect(CROSS_KIND_SORT_COLUMNS.map((c) => c.key)).toEqual(['name', 'namespace', 'status', 'age'])
    for (const c of CROSS_KIND_SORT_COLUMNS) {
      expect(c.label).toBeTruthy()
      expect(isSortableColumn(c.key, 'pods')).toBe(true)
    }
  })
})
