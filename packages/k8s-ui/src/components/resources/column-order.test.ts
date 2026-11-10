import { describe, expect, it } from 'vitest'
import { applyColumnOrder, moveColumn, pinnedColumnCount, sanitizeColumnOrder } from './column-order'

const cols = (...keys: string[]) => keys.map(key => ({ key }))
const keysOf = (list: { key: string }[]) => list.map(c => c.key)

const PODS = cols('name', 'namespace', 'ready', 'status', 'restarts', 'age', 'node')

describe('applyColumnOrder', () => {
  it('keeps definition order when no saved order is provided', () => {
    expect(keysOf(applyColumnOrder(PODS, undefined))).toEqual(keysOf(PODS))
    expect(keysOf(applyColumnOrder(PODS, null))).toEqual(keysOf(PODS))
    expect(keysOf(applyColumnOrder(PODS, []))).toEqual(keysOf(PODS))
  })

  it('applies the saved order and keeps Name first', () => {
    const order = ['age', 'restarts', 'namespace', 'ready', 'status', 'node']
    expect(keysOf(applyColumnOrder(PODS, order)))
      .toEqual(['name', 'age', 'restarts', 'namespace', 'ready', 'status', 'node'])
  })

  it('ignores a saved Name position', () => {
    const order = ['age', 'name', 'namespace', 'ready', 'status', 'restarts', 'node']
    expect(keysOf(applyColumnOrder(PODS, order))[0]).toBe('name')
  })

  it('keeps host columns that lead Name in front of it', () => {
    const fleet = cols('cluster', 'name', 'namespace', 'age')
    expect(keysOf(applyColumnOrder(fleet, ['age', 'cluster', 'namespace'])))
      .toEqual(['cluster', 'name', 'age', 'namespace'])
  })

  it('puts a column added after the save in its default position', () => {
    // 'ip' arrives between status and restarts; the user had moved age up.
    const next = cols('name', 'namespace', 'ready', 'status', 'ip', 'restarts', 'age', 'node')
    const order = ['age', 'namespace', 'ready', 'status', 'restarts', 'node']
    expect(keysOf(applyColumnOrder(next, order)))
      .toEqual(['name', 'age', 'namespace', 'ready', 'status', 'ip', 'restarts', 'node'])
  })

  it('appends a new trailing column, such as a label column, at the end', () => {
    const next = [...PODS, { key: 'label:app' }]
    const order = ['node', 'age', 'namespace', 'ready', 'status', 'restarts']
    expect(keysOf(applyColumnOrder(next, order)).at(-1)).toBe('label:app')
  })

  it('keeps several new adjacent columns in their definition order', () => {
    const next = cols('name', 'a', 'b', 'namespace', 'age')
    expect(keysOf(applyColumnOrder(next, ['age', 'namespace'])))
      .toEqual(['name', 'age', 'a', 'b', 'namespace'])
  })

  it('keeps a hidden column in place: order is independent of visibility', () => {
    const order = ['age', 'restarts', 'namespace', 'ready', 'status', 'node']
    const ordered = applyColumnOrder(PODS, order)
    const visible = new Set(['name', 'age', 'namespace', 'ready', 'status', 'node'])
    expect(keysOf(ordered.filter(c => visible.has(c.key))))
      .toEqual(['name', 'age', 'namespace', 'ready', 'status', 'node'])
    visible.add('restarts')
    expect(keysOf(ordered.filter(c => visible.has(c.key))))
      .toEqual(['name', 'age', 'restarts', 'namespace', 'ready', 'status', 'node'])
  })

  it('drops saved keys for columns that no longer exist', () => {
    const order = ['gone', 'age', 'namespace', 'ready', 'status', 'restarts', 'node']
    expect(keysOf(applyColumnOrder(PODS, order)))
      .toEqual(['name', 'age', 'namespace', 'ready', 'status', 'restarts', 'node'])
  })
})

describe('sanitizeColumnOrder', () => {
  it('reads anything that is not a key list as no order', () => {
    expect(sanitizeColumnOrder(undefined)).toBeNull()
    expect(sanitizeColumnOrder('age')).toBeNull()
    expect(sanitizeColumnOrder({ 0: 'age' })).toBeNull()
    expect(sanitizeColumnOrder([1, null])).toBeNull()
  })

  it('keeps string keys, first occurrence wins', () => {
    expect(sanitizeColumnOrder(['age', 3, 'node', 'age'])).toEqual(['age', 'node'])
  })
})

describe('moveColumn', () => {
  it('returns the movable keys with the column moved', () => {
    expect(moveColumn(PODS, 'age', 0)).toEqual(['age', 'namespace', 'ready', 'status', 'restarts', 'node'])
    expect(moveColumn(PODS, 'namespace', 5)).toEqual(['ready', 'status', 'restarts', 'age', 'node', 'namespace'])
  })

  it('returns null for a no-op, an out-of-range edge move, or a pinned column', () => {
    expect(moveColumn(PODS, 'namespace', -1)).toBeNull()
    expect(moveColumn(PODS, 'node', 99)).toBeNull()
    expect(moveColumn(PODS, 'name', 2)).toBeNull()
    expect(moveColumn(PODS, 'missing', 0)).toBeNull()
  })
})

describe('pinnedColumnCount', () => {
  it('pins through Name, and nothing when there is no Name column', () => {
    expect(pinnedColumnCount(PODS)).toBe(1)
    expect(pinnedColumnCount(cols('cluster', 'name', 'age'))).toBe(2)
    expect(pinnedColumnCount(cols('age'))).toBe(0)
  })
})
