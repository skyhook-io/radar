import { describe, expect, it } from 'vitest'
import { backupNameFor, pickDefaultStandby } from './actionModel'

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
