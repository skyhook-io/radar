import { describe, expect, it } from 'vitest'
import type { CNPGRuntimeResponse } from './cnpg'
import { cnpgInstanceLive, cnpgReplicationLive } from './cnpg-ha'

const rt = (status: any): CNPGRuntimeResponse => ({
  cluster: { namespace: 'db', name: 'pg', uid: 'u' },
  sampledAt: '2026-09-30T10:00:00Z',
  permission: { proxy: 'allowed' },
  instances: [
    { pod: 'pg-1', role: 'primary', status, metrics: { state: 'ok' } },
    { pod: 'pg-2', role: 'replica', status: { state: 'ok', replication: [] }, metrics: { state: 'ok' } },
  ],
})

describe('live replication from an incomplete primary report', () => {
  it('is unknown, never "0 streaming"', () => {
    expect(cnpgReplicationLive(rt({ state: 'partial', incomplete: true, replication: null }))).toBeUndefined()
    expect(cnpgReplicationLive(rt({ state: 'ok', replication: [] }))?.streaming).toBe(0)
  })
  it('carries the incomplete flag to the instance list', () => {
    expect(cnpgInstanceLive(rt({ state: 'partial', incomplete: true }))?.[0].incomplete).toBe(true)
  })
})
