import { describe, expect, it } from 'vitest'
import type { CNPGFleetRow } from '@skyhook-io/k8s-ui'
import type { CNPGRuntimeResponse } from './cnpg'
import { withLiveReplication } from './cnpg-ha'

function row(desired: number | null): CNPGFleetRow {
  return {
    instances: { ready: 1, desired },
    pods: [{ name: 'pg-1', role: 'primary', ready: true }],
    replication: { text: 'Lag unknown', tone: 'unknown' },
  } as unknown as CNPGFleetRow
}

function runtime(replication: { state: string; replayLag?: number }[]): CNPGRuntimeResponse {
  return {
    instances: [{ pod: 'pg-1', role: 'primary', status: { state: 'ok', capturedAt: '2026-09-30T12:00:00Z', replication } }],
  } as unknown as CNPGRuntimeResponse
}

describe('withLiveReplication', () => {
  it('counts expected standbys from spec.instances, so missing standby Pods are not healthy', () => {
    const r = withLiveReplication(row(3), runtime([]))
    expect(r.replication).toMatchObject({ tone: 'degraded', text: '0 of 2 expected standbys streaming' })
    const one = withLiveReplication(row(3), runtime([{ state: 'streaming', replayLag: 0 }]))
    expect(one.replication).toMatchObject({ tone: 'degraded', text: expect.stringContaining('1 of 2 expected standbys streaming') })
  })
  it('is healthy only when every expected standby streams', () => {
    const r = withLiveReplication(row(2), runtime([{ state: 'streaming', replayLag: 0.1 }]))
    expect(r.replication.tone).toBe('healthy')
  })
  it('is unknown when spec.instances is not reported', () => {
    expect(withLiveReplication(row(null), runtime([])).replication.tone).toBe('unknown')
  })
})
