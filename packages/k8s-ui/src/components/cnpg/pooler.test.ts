import { describe, expect, it } from 'vitest'
import { poolerPressureCoverage, poolerPressureFact, aggregatePoolerPools, observedPause, poolerBackendService, poolerReadiness, poolerPodPressure } from './pooler'

describe('poolerReadiness', () => {
  it('reads readiness from the Deployment, never from the scheduled count', () => {
    expect(poolerReadiness({ name: 'p', state: 'ok', replicas: 2, readyReplicas: 2 })).toMatchObject({ text: '2/2 ready', level: 'healthy' })
    expect(poolerReadiness({ name: 'p', state: 'ok', replicas: 2, readyReplicas: 1 }).level).toBe('degraded')
    expect(poolerReadiness({ name: 'p', state: 'ok', replicas: 2, readyReplicas: 0 }).level).toBe('unhealthy')
  })
  it('says unknown, not failed, when the Deployment is unreadable', () => {
    expect(poolerReadiness({ name: 'p', state: 'unreadable' })).toMatchObject({ text: 'Unknown', level: 'unknown' })
    expect(poolerReadiness(undefined).level).toBe('unknown')
  })
})

describe('aggregatePoolerPools', () => {
  it('sums each pool over the Pods that reported it and keeps unreported fields undefined', () => {
    const rows = aggregatePoolerPools([
      { pod: 'a', state: 'ok', pools: [{ database: 'app', user: 'app', clActive: 3, clWaiting: 1, maxwaitSeconds: 0.5, poolMode: 'transaction' }] },
      { pod: 'b', state: 'ok', pools: [{ database: 'app', user: 'app', clActive: 2, clWaiting: 0, maxwaitSeconds: 2, poolMode: 'transaction' }] },
    ])
    expect(rows).toHaveLength(1)
    expect(rows[0]).toMatchObject({ clActive: 5, clWaiting: 1, maxwaitSeconds: 2, pods: 2, poolModes: ['transaction'] })
    expect(rows[0].svActive).toBeUndefined()
  })
})

describe('observedPause', () => {
  it('distinguishes all, some and unreadable', () => {
    expect(observedPause({ state: 'ok', pods: [{ pod: 'a', state: 'ok', paused: true }, { pod: 'b', state: 'ok', paused: true }] })?.text).toBe('Paused on 2 of 2 PgBouncers')
    expect(observedPause({ state: 'ok', pods: [{ pod: 'a', state: 'ok', paused: true }, { pod: 'b', state: 'ok', paused: false }] })?.level).toBe('alert')
    expect(observedPause({ state: 'ok', pods: [{ pod: 'a', state: 'ok', paused: false }, { pod: 'b', state: 'error' }] })?.text).toBe('Serving (not paused) on 1 of 2 PgBouncers · 1 not read: b (error)')
    expect(observedPause({ state: 'denied', grant: { verb: 'create', resource: 'pods', subresource: 'exec', namespace: 'x' }, pods: [] })?.text).toContain('create pods/exec')
  })
})

describe('poolerBackendService', () => {
  it('names the Cluster Service by type', () => {
    expect(poolerBackendService('pg', 'rw')).toBe('pg-rw')
    expect(poolerBackendService('pg', 'ro')).toBe('pg-ro')
    expect(poolerBackendService('pg', undefined)).toBeUndefined()
  })
})

describe('poolerPodPressure', () => {
  it('totals each Pod on its own so one queuing Pod is visible', () => {
    const rows = poolerPodPressure([
      { pod: 'pooler-b', state: 'ok', pools: [{ database: 'app', user: 'app', clActive: 2, clWaiting: 0, svActive: 1 }, { database: 'app', user: 'ro', clActive: 1, clWaiting: 0, svActive: 0 }] },
      { pod: 'pooler-a', state: 'ok', pools: [{ database: 'app', user: 'app', clActive: 20, clWaiting: 15, svActive: 20, maxwaitSeconds: 4.2 }] },
      { pod: 'pooler-c', state: 'unreachable', error: 'no answer within 5s' },
    ] as never)
    expect(rows).toEqual([
      { pod: 'pooler-a', state: 'ok', error: undefined, clActive: 20, clWaiting: 15, svActive: 20, maxwaitSeconds: 4.2 },
      { pod: 'pooler-b', state: 'ok', error: undefined, clActive: 3, clWaiting: 0, svActive: 1 },
      { pod: 'pooler-c', state: 'unreachable', error: 'no answer within 5s' },
    ])
  })
})

describe('pooler pressure certainty', () => {
  it('says idle only when every Pod answered in full', () => {
    const a = { pod: 'a', state: 'ok', pools: [] }
    expect(poolerPressureCoverage([a]).empty).toBe('Idle: no client pools open')
    for (const state of ['partial', 'unreachable']) {
      const c = poolerPressureCoverage([a, { pod: 'b', state, reason: 'pool list capped' }])
      expect(c.complete).toBe(false)
      expect(c.empty).toBe('No pools seen in what was read')
      expect(c.limitation).toContain('b:')
      expect(c.limitation).toContain('pool list capped')
    }
  })
  it('shows unknown for partial zero and a lower bound for partial positive counts', () => {
    const p = { pod: 'a', state: 'partial', pools: [{ database: 'app', user: 'app', clWaiting: 0, clActive: 3 }] }
    expect(poolerPressureFact([p], 'clWaiting')).toMatchObject({ text: 'Unknown', tone: 'unknown' })
    expect(poolerPressureFact([p], 'clActive')).toMatchObject({ text: '≥3', tone: 'unknown' })
    expect(poolerPressureFact([{ ...p, state: 'ok' }], 'clWaiting').text).toBe('0')
    expect(poolerPressureFact([{ ...p, state: 'ok' }, { pod: 'b', state: 'unreachable' }], 'clWaiting').text).toBe('Unknown')
  })
  it('qualifies a total when a pool omitted the field, even with complete Pod reads', () => {
    const pods = [{ pod: 'a', state: 'ok', pools: [{ database: 'app', user: 'a', clActive: 2 }, { database: 'app', user: 'b' }] }]
    expect(poolerPressureFact(pods, 'clActive').text).toBe('≥2')
    expect(poolerPressureFact(pods, 'clWaiting').text).toBe('Unknown')
  })
})
