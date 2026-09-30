import { describe, expect, it } from 'vitest'
import type { CNPGFleetRow } from '@skyhook-io/k8s-ui'
import type { CNPGRuntimeResponse } from './cnpg'
import { cnpgInstanceLiveUnavailable, cnpgReplicationGap, withLiveReplication } from './cnpg-ha'

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

describe('cnpgInstanceLiveUnavailable', () => {
  const rt = (proxy: 'allowed' | 'denied', grant?: string) => ({ permission: { proxy, grant }, instances: [] }) as unknown as CNPGRuntimeResponse
  it('names the grant only when the proxy is denied', () => {
    expect(cnpgInstanceLiveUnavailable(rt('denied', 'get pods/proxy in db'), undefined)).toBe('needs get pods/proxy in db')
    expect(cnpgInstanceLiveUnavailable(rt('allowed'), undefined)).toBeUndefined()
    expect(cnpgInstanceLiveUnavailable(undefined, undefined)).toBe('instance managers not read yet')
    expect(cnpgInstanceLiveUnavailable(undefined, new Error('boom'))).toBe('the runtime read failed: boom')
  })
})

describe('cnpgReplicationGap', () => {
  it('says why the primary’s replication rows are missing', () => {
    const rt = (instances: unknown[]) => ({ permission: { proxy: 'allowed' }, instances }) as unknown as CNPGRuntimeResponse
    expect(cnpgReplicationGap(rt([{ pod: 'pg-1', role: 'primary', status: { state: 'unreachable', error: 'PostgreSQL is not running on this instance' } }]), undefined)).toBe(
      'pg-1 did not report (PostgreSQL is not running on this instance)',
    )
    expect(cnpgReplicationGap(rt([{ pod: 'pg-1', role: 'primary', status: { state: 'ok', replication: [] } }]), undefined)).toBeUndefined()
    expect(cnpgReplicationGap({ permission: { proxy: 'denied', grant: 'get pods/proxy in db' }, instances: [] } as unknown as CNPGRuntimeResponse, undefined)).toBe(
      'needs get pods/proxy in db',
    )
  })
})

describe('withLiveReplication with a missing standby', () => {
  it('takes the worse of the missing standby and the lag', () => {
    const rt = {
      permission: { proxy: 'allowed' },
      instances: [{ pod: 'pg-1', role: 'primary', status: { state: 'ok', capturedAt: 't', replication: [{ applicationName: 'pg-2', state: 'streaming', replayLag: 72 }] } }],
    } as unknown as CNPGRuntimeResponse
    const out = withLiveReplication(row(3), rt)
    expect(out.replication.tone).toBe('unhealthy')
    expect(out.replication.text).toContain('1 of 2 expected standbys streaming')
  })
})
