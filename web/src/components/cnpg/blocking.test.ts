import { describe, expect, it } from 'vitest'
import { buildBlockingTree, countVictims } from './blocking'
import type { CNPGBackend } from '../../api/cnpg-sessions'

const s = (pid: number, blockedBy: number[] = []): CNPGBackend => ({ pid, blockedBy, backendStart: `t${pid}` })

describe('buildBlockingTree', () => {
  it('nests a chain under its root blocker', () => {
    const roots = buildBlockingTree([s(1), s(2, [1]), s(3, [2])])
    expect(roots).toHaveLength(1)
    expect(roots[0].session.pid).toBe(1)
    expect(roots[0].children[0].session.pid).toBe(2)
    expect(roots[0].children[0].children[0].session.pid).toBe(3)
    expect(countVictims(roots[0])).toBe(2)
  })

  it('shows a victim of two blockers under both, naming the other', () => {
    const roots = buildBlockingTree([s(1), s(2), s(3, [1, 2])])
    expect(roots.map((r) => r.session.pid)).toEqual([1, 2])
    expect(roots[0].children[0].alsoWaitsOn).toEqual([2])
    expect(roots[1].children[0].alsoWaitsOn).toEqual([1])
  })

  it('keeps a victim whose blocker is not listed', () => {
    const roots = buildBlockingTree([s(5, [99])])
    expect(roots).toHaveLength(1)
    expect(roots[0].session.pid).toBe(5)
    expect(roots[0].cycle).toBeUndefined()
  })

  it('marks a deadlock cycle instead of recursing forever', () => {
    const roots = buildBlockingTree([s(1, [2]), s(2, [1])])
    expect(roots.length).toBeGreaterThan(0)
    expect(roots.every((r) => r.cycle)).toBe(true)
  })
})
