import { describe, expect, it } from 'vitest'
import { aggregatePoolerPools, observedPause, poolerBackendService, poolerReadiness } from './pooler'

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
    expect(observedPause({ state: 'ok', pods: [{ pod: 'a', state: 'ok', paused: false }, { pod: 'b', state: 'error' }] })?.text).toBe('Serving (not paused) on 1 of 2 PgBouncers · 1 not read')
    expect(observedPause({ state: 'denied', grant: 'create pods/exec in namespace x', pods: [] })?.text).toContain('create pods/exec')
  })
})

describe('poolerBackendService', () => {
  it('names the Cluster Service by type', () => {
    expect(poolerBackendService('pg', 'rw')).toBe('pg-rw')
    expect(poolerBackendService('pg', 'ro')).toBe('pg-ro')
    expect(poolerBackendService('pg', undefined)).toBeUndefined()
  })
})
