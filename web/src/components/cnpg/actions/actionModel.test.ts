import { describe, expect, it } from 'vitest'
import { backupNameFor, cnpgDestroyBlocker, pickDefaultStandby, switchoverCandidateFacts, switchoverConcernWarning, switchoverConcerns, switchoverDefault, switchoverLagNote } from './actionModel'

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
    expect(cnpgDestroyBlocker({ allowed: false, permission: 'denied', reason: 'Needs delete pods in db', grant: { verb: 'delete', resource: 'pods', namespace: 'db' } }, 'pg-2', true)).toBe('Needs delete pods in db')
    expect(cnpgDestroyBlocker({ allowed: false, permission: 'allowed', reason: 'The cluster is hibernated' }, 'pg-2', true)).toBe('The cluster is hibernated')
    expect(cnpgDestroyBlocker({ allowed: true, permission: 'allowed' }, 'pg-2', true)).toBeUndefined()
  })
})

describe('switchoverLagNote', () => {
  it('warns from the WAL still to replay, never from replay delay alone', () => {
    expect(switchoverLagNote({ pod: 'pg-2', replayBacklogBytes: 16 * 1024 * 1024, state: 'streaming' })).toBe('pg-2 has 16 MiB of WAL still to replay; the switchover may take longer while it catches up.')
    expect(switchoverLagNote({ pod: 'pg-2', replayBacklogBytes: 16 * 1024 * 1024 })).toBe("pg-2 has 16 MiB of WAL still to replay and isn't connected to the primary to receive it.")
    expect(switchoverLagNote({ pod: 'pg-2', replayBacklogBytes: 0 })).toBeUndefined()
    expect(switchoverLagNote({ pod: 'pg-2' })).toBeUndefined()
  })
  it('lists the backlog before the replay delay on the candidate line', () => {
    expect(switchoverCandidateFacts({ replayBacklogBytes: 0, replayLagSeconds: 12 })).toEqual(['backlog 0 B', 'replay delay 12 s'])
  })
})

describe('pickDefaultStandby by backlog', () => {
  it('prefers the standby with less WAL to replay over a lower replay delay', () => {
    const a = { pod: 'a', podUID: '1', replayBacklogBytes: 0, replayLagSeconds: 12 }
    const b = { pod: 'b', podUID: '2', replayBacklogBytes: 4096, replayLagSeconds: 1 }
    expect(pickDefaultStandby([b, a])?.pod).toBe('a')
  })
})

describe('switchoverDefault', () => {
  const before = [
    { pod: 'a', podUID: '1' },
    { pod: 'b', podUID: '2' },
  ]
  const after = [
    { pod: 'a', podUID: '1', replayBacklogBytes: 4096 },
    { pod: 'b', podUID: '2', replayBacklogBytes: 0 },
  ]
  it('re-picks the default when runtime data arrives, until the user chooses', () => {
    expect(switchoverDefault({ touched: false, current: 'a', standbys: before })).toBe('a')
    expect(switchoverDefault({ touched: false, current: 'a', standbys: after })).toBe('b')
    expect(switchoverDefault({ touched: true, current: 'a', standbys: after })).toBe('a')
  })
})

describe('switchover concerns', () => {
  const primary = { state: 'ok', timeline: 7, replication: [{ applicationName: 'pg-2' }] }
  it('names a standby that is not connected, paused, or on another timeline', () => {
    expect(switchoverConcerns(primary, { pod: 'pg-2', status: { state: 'ok', timeline: 7 } })).toEqual([])
    expect(switchoverConcerns(primary, { pod: 'pg-1', status: { state: 'ok', timeline: 4, replayPaused: true } })).toEqual([
      'not connected to the primary',
      'replay paused',
      'timeline 4, primary on 7',
    ])
    // Without the primary's replication rows, connection is not judged.
    expect(switchoverConcerns({ state: 'partial', timeline: 7, replication: null }, { pod: 'pg-1', status: { state: 'ok', timeline: 7 } })).toEqual([])
  })
  it('never picks a standby with concerns by default while a clean one exists', () => {
    expect(
      pickDefaultStandby([
        { pod: 'pg-1', podUID: '1', concerns: ['not connected to the primary'] },
        { pod: 'pg-2', podUID: '2', replayBacklogBytes: 1024, syncState: 'async' },
      ])?.pod,
    ).toBe('pg-2')
    expect(pickDefaultStandby([{ pod: 'pg-1', podUID: '1', concerns: ['replay paused'] }])).toBeUndefined()
  })
  it('warns with what was observed, not with a guess at the outcome', () => {
    const w = switchoverConcernWarning('pg-1', ['not connected to the primary', 'replay paused'])!
    expect(w).toContain('pg-1: not connected to the primary · replay paused.')
    expect(w).toContain('not receiving WAL from the current primary')
    expect(switchoverConcernWarning('pg-1', [])).toBeNull()
  })
})
