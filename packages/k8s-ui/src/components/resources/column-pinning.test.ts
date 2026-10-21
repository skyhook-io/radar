import { describe, expect, it } from 'vitest'
import { moveResourceColumn, orderResourceColumns, resolveColumnPins, sanitizePinnedColumns, toggleResourceColumnPin } from './column-pinning'

const columns = ['name', 'namespace', 'status', 'age'].map(key => ({ key, width: 200 }))
const keys = (items: readonly { key: string }[]) => items.map(column => column.key)

describe('resource column layout', () => {
  it('moves a last column to the pinned group in both the editor and table order', () => {
    const next = toggleResourceColumnPin(columns, [], 'age')
    expect(next).toEqual({ order: ['age', 'name', 'namespace', 'status'], pinned: ['age'] })
    expect(keys(orderResourceColumns(columns, next.order, next.pinned))).toEqual(next.order)
  })

  it('appends a new pin and unpins into the first scrolling position', () => {
    const next = toggleResourceColumnPin(columns, ['status'], 'age')
    expect(next.pinned).toEqual(['status', 'age'])
    const ordered = orderResourceColumns(columns, next.order, next.pinned)
    const unpinned = toggleResourceColumnPin(ordered, next.pinned, 'status')
    expect(unpinned).toEqual({ order: ['age', 'status', 'name', 'namespace'], pinned: ['age'] })
  })

  it('preserves requested order when pins no longer fit, pausing a contiguous suffix', () => {
    const varied = [{ key: 'age', width: 200 }, { key: 'name', width: 400 }, { key: 'status', width: 100 }]
    const order = ['age', 'name', 'status']
    expect([...resolveColumnPins(varied, order, 740).offsets.keys()]).toEqual(['age'])
    expect(resolveColumnPins(varied, order, 740).paused).toEqual(['name', 'status'])
    expect(keys(orderResourceColumns(varied, order, order))).toEqual(order)
    expect([...resolveColumnPins(varied, order, 1000).offsets.keys()]).toEqual(order)
  })

  it('reserves scrolling space and includes selection or compare controls in offsets', () => {
    const resolved = resolveColumnPins(columns, ['name', 'status'], 700, 40)
    expect([...resolved.offsets]).toEqual([['name', 40], ['status', 240]])
    expect(resolveColumnPins(columns, ['name'], 300, 40).paused).toEqual(['name'])
  })

  it('ignores removed columns, inserts new fields before their default anchor, and retains hidden order', () => {
    const offered = [...columns.slice(0, 2), { key: 'new', width: 100 }, ...columns.slice(2)]
    expect(keys(orderResourceColumns(offered, ['age', 'gone', 'name', 'namespace', 'status'], ['gone', 'status'])))
      .toEqual(['status', 'age', 'name', 'namespace', 'new'])
  })

  it('sanitizes saved pin preferences', () => {
    expect(sanitizePinnedColumns(['name', null, 3, 'name', '', '  ', 'label:app'])).toEqual(['name', 'label:app'])
    expect(sanitizePinnedColumns({ name: true })).toEqual([])
  })

  it('reorders inside each group and stops at the pin boundary', () => {
    expect(moveResourceColumn(columns, ['name', 'namespace'], 'namespace', 0)).toEqual(['namespace', 'name', 'status', 'age'])
    expect(moveResourceColumn(columns, ['name', 'namespace'], 'status', 0)).toBeNull()
    expect(moveResourceColumn(columns, ['name', 'namespace'], 'namespace', 3)).toBeNull()
    expect(moveResourceColumn(columns, ['name', 'namespace'], 'age', 0)).toEqual(['name', 'namespace', 'age', 'status'])
  })

  it('keeps host context ahead of pinned and scrolling resource columns', () => {
    const fleet = [{ key: 'cluster', width: 192 }, ...columns]
    expect(keys(orderResourceColumns(fleet, ['age', 'name', 'cluster', 'status', 'namespace'], ['age'], 1)))
      .toEqual(['cluster', 'age', 'name', 'status', 'namespace'])
    const next = toggleResourceColumnPin(fleet, [], 'name', 1)
    expect(next.order[0]).toBe('cluster')
    expect(moveResourceColumn(fleet, ['cluster', 'name'], 'name', 0, 1)).toBeNull()
    expect(moveResourceColumn(fleet, [], 'cluster', 3, 1)).toBeNull()
    expect(moveResourceColumn(fleet, [], 'age', 0, 1)).toEqual(['cluster', 'age', 'name', 'namespace', 'status'])
  })
})
