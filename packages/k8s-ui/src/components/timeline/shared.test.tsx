import { describe, it, expect } from 'vitest'
import {
  buildHealthSpans,
  sweepAggregateHealth,
  formatAggregateHealthTooltip,
  buildLaneMemberSpans,
  type MemberHealthSpans,
  type AggregateHealthSegment,
} from './shared'
import type { ResourceLane } from '../../utils/resource-hierarchy'

describe('buildHealthSpans own-resource existence (child cleanup must not kill the lane)', () => {
  const t = (min: number) => new Date(Date.UTC(2026, 0, 1, 10, min)).toISOString()
  const start = Date.UTC(2026, 0, 1, 10, 0)
  const now = Date.UTC(2026, 0, 1, 11, 0)
  const cron = { kind: 'CronJob', namespace: 'team-a', name: 'aliaser' }
  const ev = (over: Record<string, unknown>) => ({
    id: String(Math.random()), source: 'informer', eventType: 'update',
    kind: 'CronJob', namespace: 'team-a', name: 'aliaser',
    timestamp: t(5), healthState: 'idle', ...over,
  }) as any

  it('a child Job delete does not truncate the parent lane existence', () => {
    const events = [
      ev({ timestamp: t(1) }),
      ev({ kind: 'Job', name: 'aliaser-123', eventType: 'delete', timestamp: t(10), healthState: undefined }),
      ev({ timestamp: t(20) }),
    ]
    const { spans } = buildHealthSpans(events, start, now, events, cron)
    // strip must reach `now`, not stop at the child delete
    expect(spans[spans.length - 1].end).toBe(now)
  })

  it('the lane resource own delete still ends its existence', () => {
    const events = [
      ev({ timestamp: t(1) }),
      ev({ eventType: 'delete', timestamp: t(30), healthState: undefined }),
    ]
    const { spans } = buildHealthSpans(events, start, now, events, cron)
    expect(spans[spans.length - 1].end).toBe(Date.UTC(2026, 0, 1, 10, 30))
  })
})

describe('buildHealthSpans birth clamp (no pre-birth back-fill)', () => {
  const start = Date.UTC(2026, 0, 1, 10, 0)
  const now = Date.UTC(2026, 0, 1, 12, 40)
  const pod = { kind: 'Pod', namespace: 'team-a', name: 'runner-abc12' }
  const ev = (min: number, over: Record<string, unknown>) => ({
    id: `${min}-${JSON.stringify(over).length}`, source: 'informer', kind: 'Pod', namespace: 'team-a', name: 'runner-abc12',
    timestamp: new Date(Date.UTC(2026, 0, 1, 12, min)).toISOString(), eventType: 'update', ...over,
  }) as any

  it('a pod whose first event is its creation gets no span before birth', () => {
    const events = [
      ev(25, { reason: 'created', healthState: 'unknown' }),
      ev(25, { eventType: 'add', healthState: 'healthy' }),
      ev(30, { healthState: 'neutral' }),
    ]
    const { spans } = buildHealthSpans(events, start, now, events, pod)
    expect(spans[0].start).toBe(Date.UTC(2026, 0, 1, 12, 25))
  })

  it('a resource first seen via a later mutation keeps the existed-before assumption', () => {
    const events = [ev(25, { healthState: 'healthy' })]
    const { spans } = buildHealthSpans(events, start, now, events, pod)
    expect(spans[0].start).toBe(start)
  })
})

// --- state-family aggregate health sweep (item 3) ---------------------------

const member = (name: string, ...spans: [number, number, string][]): MemberHealthSpans => ({
  name,
  spans: spans.map(([start, end, health]) => ({ start, end, health })),
})
// A member healthy across the whole [0,100] window.
const full = (name: string, health: string): MemberHealthSpans => member(name, [0, 100, health])

describe('sweepAggregateHealth: state families', () => {
  it('uniform OK (all healthy) → one healthy segment, not mixed', () => {
    const segs = sweepAggregateHealth([full('a', 'healthy'), full('b', 'healthy')], 0, 100)
    expect(segs).toHaveLength(1)
    expect(segs[0]).toMatchObject({ health: 'healthy', mixed: false, total: 2 })
    expect(segs[0].byState).toEqual({ healthy: ['a', 'b'] })
  })

  it('uniform BAD (degraded + unhealthy) → one BAD family segment painting the most severe (unhealthy)', () => {
    const segs = sweepAggregateHealth([full('a', 'degraded'), full('b', 'unhealthy')], 0, 100)
    expect(segs).toHaveLength(1)
    expect(segs[0]).toMatchObject({ health: 'unhealthy', mixed: false, total: 2 })
  })

  it('benign mix (healthy + idle) stays ONE family (OK) → NOT mixed [founder key case]', () => {
    const segs = sweepAggregateHealth([full('a', 'healthy'), full('b', 'idle')], 0, 100)
    expect(segs).toHaveLength(1)
    expect(segs[0].mixed).toBe(false)
    // Tie between healthy and idle → healthy.
    expect(segs[0].health).toBe('healthy')
  })

  it('OK majority wins (2 idle vs 1 healthy → idle), no tie', () => {
    const segs = sweepAggregateHealth([full('a', 'idle'), full('b', 'idle'), full('c', 'healthy')], 0, 100)
    expect(segs[0]).toMatchObject({ health: 'idle', mixed: false })
  })

  it('true mix (healthy + unhealthy) → MIXED', () => {
    const segs = sweepAggregateHealth([full('a', 'healthy'), full('b', 'unhealthy')], 0, 100)
    expect(segs).toHaveLength(1)
    expect(segs[0]).toMatchObject({ health: 'mixed', mixed: true })
  })

  it('rolling + healthy → MIXED (rolling is its own family)', () => {
    const segs = sweepAggregateHealth([full('a', 'rolling'), full('b', 'healthy')], 0, 100)
    expect(segs[0]).toMatchObject({ health: 'mixed', mixed: true })
  })

  it('uniform rolling → one rolling segment, not mixed', () => {
    const segs = sweepAggregateHealth([full('a', 'rolling'), full('b', 'rolling')], 0, 100)
    expect(segs[0]).toMatchObject({ health: 'rolling', mixed: false })
  })
})

describe('sweepAggregateHealth: slice boundaries + attribution', () => {
  it('splits at a member transition, painting each slice from that slice state set', () => {
    // a healthy throughout; b healthy [0,60] then unhealthy [60,100].
    const segs = sweepAggregateHealth(
      [full('a', 'healthy'), member('b', [0, 60, 'healthy'], [60, 100, 'unhealthy'])],
      0, 100,
    )
    expect(segs).toHaveLength(2)
    expect(segs[0]).toMatchObject({ start: 0, end: 60, health: 'healthy', mixed: false })
    expect(segs[1]).toMatchObject({ start: 60, end: 100, health: 'mixed', mixed: true })
  })

  it('merges adjacent slices with identical state + attribution', () => {
    // Boundary at 50 exists in the data but both slices resolve identically → merge.
    const segs = sweepAggregateHealth(
      [member('a', [0, 50, 'healthy'], [50, 100, 'healthy'])],
      0, 100,
    )
    expect(segs).toHaveLength(1)
    expect(segs[0]).toMatchObject({ start: 0, end: 100, health: 'healthy' })
  })

  it('paints nothing where no member is alive (gap slice omitted)', () => {
    const segs = sweepAggregateHealth([member('a', [0, 40, 'healthy'], [70, 100, 'healthy'])], 0, 100)
    // Two live slices, the [40,70] gap is dropped.
    expect(segs.map((s) => [s.start, s.end])).toEqual([[0, 40], [70, 100]])
  })

  it('records per-state member names for attribution', () => {
    const segs = sweepAggregateHealth(
      [full('u1', 'unhealthy'), full('h1', 'healthy'), full('h2', 'healthy')],
      0, 100,
    )
    expect(segs[0].byState).toEqual({ unhealthy: ['u1'], healthy: ['h1', 'h2'] })
    expect(segs[0].total).toBe(3)
  })
})

describe('formatAggregateHealthTooltip', () => {
  const seg = (over: Partial<AggregateHealthSegment>): AggregateHealthSegment => ({
    start: 0, end: 100, health: 'healthy', mixed: false, total: 1, byState: { healthy: ['a'] }, ...over,
  })

  it('uniform → "healthy · 23/23"', () => {
    const names = Array.from({ length: 23 }, (_, i) => `p${i}`)
    expect(formatAggregateHealthTooltip(seg({ total: 23, byState: { healthy: names } })))
      .toBe('healthy · 23/23')
  })

  it('mixed → "mixed · 2/23 unhealthy: <names> · 21 healthy/idle"', () => {
    const healthy = Array.from({ length: 21 }, (_, i) => `h${i}`)
    const s = seg({ health: 'mixed', mixed: true, total: 23, byState: { unhealthy: ['name-a', 'name-b'], healthy } })
    expect(formatAggregateHealthTooltip(s)).toBe('mixed · 2/23 unhealthy: name-a, name-b · 21 healthy/idle')
  })

  it('caps callout names at 3 + "+N more"', () => {
    const s = seg({ health: 'mixed', mixed: true, total: 6, byState: { unhealthy: ['u1', 'u2', 'u3', 'u4', 'u5'], healthy: ['h1'] } })
    expect(formatAggregateHealthTooltip(s)).toBe('mixed · 5/6 unhealthy: u1, u2, u3, +2 more · 1 healthy/idle')
  })

  it('rolling callout in a mixed slice is labelled rolling', () => {
    const s = seg({ health: 'mixed', mixed: true, total: 3, byState: { rolling: ['r1'], healthy: ['h1', 'h2'] } })
    expect(formatAggregateHealthTooltip(s)).toBe('mixed · 1/3 rolling: r1 · 2 healthy/idle')
  })
})

describe('buildLaneMemberSpans (leaf gathering)', () => {
  const win = { start: Date.UTC(2026, 0, 1, 10, 0), now: Date.UTC(2026, 0, 1, 11, 0) }
  const ev = (kind: string, name: string, health: string): any => ({
    id: `${name}-add`, source: 'informer', kind, namespace: 'ops', name,
    timestamp: new Date(Date.UTC(2026, 0, 1, 10, 10)).toISOString(), eventType: 'add', healthState: health,
  })
  const leaf = (kind: string, name: string, health: string): ResourceLane => ({
    id: `${kind}/ops/${name}`, kind, namespace: 'ops', name, isWorkload: true, events: [ev(kind, name, health)], children: [],
  })

  it('returns one member per LEAF resource (grandchildren, not intermediate nodes)', () => {
    const job: ResourceLane = {
      id: 'Job/ops/j', kind: 'Job', namespace: 'ops', name: 'j', isWorkload: true, events: [],
      children: [leaf('Pod', 'j-abc12', 'healthy'), leaf('Pod', 'j-def34', 'unhealthy')],
    }
    const cron: ResourceLane = {
      id: 'CronJob/ops/backup', kind: 'CronJob', namespace: 'ops', name: 'backup', isWorkload: true, events: [], children: [job],
    }
    const members = buildLaneMemberSpans(cron, win.start, win.now)
    expect(members.map((m) => m.name).sort()).toEqual(['j-abc12', 'j-def34'])
    // Feeding these into the sweep yields a MIXED strip (healthy pod vs unhealthy pod).
    const segs = sweepAggregateHealth(members, win.start, win.now)
    expect(segs.some((s) => s.mixed)).toBe(true)
  })

  it('a childless lane is its own single leaf member', () => {
    const dep = leaf('Deployment', 'web', 'healthy')
    const members = buildLaneMemberSpans(dep, win.start, win.now)
    expect(members.map((m) => m.name)).toEqual(['web'])
  })
})
