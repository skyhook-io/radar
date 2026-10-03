import { describe, expect, it } from 'vitest'
import type { CNPGRuntimeResponse } from '../../api/cnpg'
import { cnpgPublisherSlotsFrom } from './logicalSlots'

const rt = (status: any, proxy: 'allowed' | 'denied' = 'allowed', role: 'primary' | 'replica' = 'primary'): CNPGRuntimeResponse => ({
  cluster: { namespace: 'src', name: 'src', uid: 'u' },
  sampledAt: '2026-09-30T10:00:00Z',
  permission: { proxy, grant: 'get pods/proxy in src' },
  instances: [{ pod: 'src-1', role, status, metrics: { state: 'ok' } }],
})

describe('cnpgPublisherSlotsFrom', () => {
  it('keeps unread, denied and unreachable apart from an empty slot list', () => {
    expect(cnpgPublisherSlotsFrom(undefined).state).toBe('notRead')
    expect(cnpgPublisherSlotsFrom(rt({ state: 'ok' }, 'denied')).state).toBe('denied')
    expect(cnpgPublisherSlotsFrom(rt({ state: 'unreachable', error: 'timeout' })).reason).toBe('timeout')
    expect(cnpgPublisherSlotsFrom(rt({ state: 'ok' }, 'allowed', 'replica')).state).toBe('unavailable')
  })
  it('passes the primary slots through', () => {
    const r = cnpgPublisherSlotsFrom(rt({ state: 'ok', slots: [{ name: 'orders_sub', type: 'logical', active: true, retainedBytes: 10 }] }))
    expect(r).toEqual({ state: 'ok', slots: [{ name: 'orders_sub', type: 'logical', active: true, walStatus: undefined, retainedBytes: 10, database: undefined }] })
  })
  it('keeps a capped report partial and marks cached data after a failed refresh', () => {
    const capped = cnpgPublisherSlotsFrom(rt({ state: 'partial', reason: '250 replication slots; the first 200 are shown', slots: [] }))
    expect(capped.state).toBe('partial')
    expect(capped.reason).toContain('250')
    expect(cnpgPublisherSlotsFrom(rt({ state: 'ok', slots: [] }), new Error('boom'), true).stale).toBe(true)
    expect(cnpgPublisherSlotsFrom(rt({ state: 'partial', incomplete: true, slots: null })).state).toBe('unavailable')
  })
})
