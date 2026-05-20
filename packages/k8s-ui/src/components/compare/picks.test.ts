import { describe, it, expect } from 'vitest'
import { togglePick, pickIndex, COMPARE_PICK_CAP } from './picks'
import type { NamespacedRef } from './types'

const ref = (namespace: string, name: string): NamespacedRef => ({ namespace, name })

describe('togglePick', () => {
  it('adds first pick', () => {
    expect(togglePick([], ref('prod', 'api'))).toEqual([ref('prod', 'api')])
  })

  it('adds second pick after first (A,B order preserved)', () => {
    const after = togglePick([ref('prod', 'api')], ref('staging', 'api'))
    expect(after).toEqual([ref('prod', 'api'), ref('staging', 'api')])
  })

  it('removes existing pick (deselect)', () => {
    const start: NamespacedRef[] = [ref('prod', 'api'), ref('staging', 'api')]
    expect(togglePick(start, ref('prod', 'api'))).toEqual([ref('staging', 'api')])
  })

  it('replaces oldest when at cap — clicking a third row keeps the click visible', () => {
    const start: NamespacedRef[] = [ref('prod', 'api'), ref('staging', 'api')]
    expect(togglePick(start, ref('dev', 'api'))).toEqual([ref('staging', 'api'), ref('dev', 'api')])
  })

  it('treats cluster-scoped (ns="") with same name as same pick', () => {
    const start: NamespacedRef[] = [ref('', 'cluster-admin')]
    expect(togglePick(start, ref('', 'cluster-admin'))).toEqual([])
  })

  it('treats same name in different namespaces as different picks', () => {
    const start: NamespacedRef[] = [ref('prod', 'api')]
    expect(togglePick(start, ref('staging', 'api'))).toEqual([ref('prod', 'api'), ref('staging', 'api')])
  })

  it('ignores ref without a name (defensive against bad row data)', () => {
    const start: NamespacedRef[] = [ref('prod', 'api')]
    expect(togglePick(start, ref('prod', ''))).toBe(start)
  })

  it('does not mutate the input array', () => {
    const start: NamespacedRef[] = [ref('prod', 'api')]
    const out = togglePick(start, ref('staging', 'api'))
    expect(start).toEqual([ref('prod', 'api')])
    expect(out).not.toBe(start)
  })

  it('cap is 2', () => {
    expect(COMPARE_PICK_CAP).toBe(2)
  })
})

describe('pickIndex', () => {
  it('returns -1 when not in list', () => {
    expect(pickIndex([], ref('prod', 'api'))).toBe(-1)
  })

  it('returns 0 for the first slot (A)', () => {
    expect(pickIndex([ref('prod', 'api'), ref('staging', 'api')], ref('prod', 'api'))).toBe(0)
  })

  it('returns 1 for the second slot (B)', () => {
    expect(pickIndex([ref('prod', 'api'), ref('staging', 'api')], ref('staging', 'api'))).toBe(1)
  })

  it('returns -1 for ref with empty name', () => {
    expect(pickIndex([ref('prod', 'api')], ref('prod', ''))).toBe(-1)
  })
})
