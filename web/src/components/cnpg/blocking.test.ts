import { describe, expect, it } from 'vitest'
import { buildBlockingTree, cnpgConnectionFigure, cnpgMetricsApiMissing, countVictims } from './blocking'
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

describe('cnpgConnectionFigure', () => {
  it('states usable headroom from pg_stat_activity when exec read it', () => {
    expect(cnpgConnectionFigure({ maxConnections: 100, superuserReservedConnections: 3, clientBackends: 6 }, { sessionsTotal: 6, maxConnections: 100 })).toMatchObject({
      value: '6 of 97 usable',
      detail: expect.stringContaining('max_connections 100, 3 reserved for superusers'),
    })
  })
  it('falls back to the exporter count and says the reserve is unknown', () => {
    const f = cnpgConnectionFigure(undefined, { sessionsTotal: 90, maxConnections: 100 })
    expect(f).toMatchObject({ value: '90 of 100', tone: 'degraded' })
    expect(f?.detail).toContain('superuser reserve is not read')
    expect(cnpgConnectionFigure(undefined, {})).toBeUndefined()
  })
})

describe('cnpgMetricsApiMissing', () => {
  it('says the metrics API is missing only when no instance has metrics', () => {
    expect(cnpgMetricsApiMissing([null, null])).toBe(true)
    expect(cnpgMetricsApiMissing([null, { containers: [] }])).toBe(false)
    expect(cnpgMetricsApiMissing([null, undefined])).toBe(false)
    expect(cnpgMetricsApiMissing([])).toBe(false)
  })
})
