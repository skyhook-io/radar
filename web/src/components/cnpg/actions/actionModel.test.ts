import { describe, expect, it } from 'vitest'
import { backupNameFor, cnpgDestroyBlocker, pickDefaultStandby } from './actionModel'

describe('CNPG action model', () => {
  it('names backups like kubectl-cnpg, in UTC', () => {
    expect(backupNameFor('pg-orders', new Date(Date.UTC(2026, 8, 28, 14, 5, 9)))).toBe('pg-orders-20260928140509')
  })

  it('prefers a synchronous standby, then the least lag, and never an ineligible one', () => {
    expect(
      pickDefaultStandby([
        { pod: 'a', podUID: '1', replayLagSeconds: 0.1, syncState: 'async' },
        { pod: 'b', podUID: '2', replayLagSeconds: 3, syncState: 'sync' },
        { pod: 'c', podUID: '3', ineligible: 'fenced', syncState: 'sync' },
      ])?.pod,
    ).toBe('b')
    expect(pickDefaultStandby([{ pod: 'a', podUID: '1', ineligible: 'not ready' }])).toBeUndefined()
  })
})

describe('cnpgDestroyBlocker', () => {
  const fence = { allowed: false, permission: 'allowed' as const, reason: 'Fence pg-2 first: a fenced instance cannot be promoted while it is destroyed' }
  it('leaves the missing fence to the Fence-first callout', () => {
    expect(cnpgDestroyBlocker(fence, 'pg-2', true)).toBeUndefined()
    expect(cnpgDestroyBlocker(fence, 'pg-2', false)).toBe(fence.reason)
  })
  it('keeps real blockers as the alert', () => {
    expect(cnpgDestroyBlocker({ allowed: false, permission: 'denied', reason: 'Needs delete pods in db', grant: 'delete pods in db' }, 'pg-2', true)).toBe('Needs delete pods in db')
    expect(cnpgDestroyBlocker({ allowed: false, permission: 'allowed', reason: 'The cluster is hibernated' }, 'pg-2', true)).toBe('The cluster is hibernated')
    expect(cnpgDestroyBlocker({ allowed: true, permission: 'allowed' }, 'pg-2', true)).toBeUndefined()
  })
})
