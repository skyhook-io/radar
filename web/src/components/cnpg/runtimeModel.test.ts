import { describe, expect, it } from 'vitest'
import type { CNPGRuntimeInstance, CNPGRuntimeReplication } from '../../api/cnpg'
import { cnpgCheckpointView, cnpgDatabaseHealthRows, cnpgStandbyBacklogTone, cnpgStandbyHeadline } from './runtimeModel'

describe('cnpgCheckpointView', () => {
  it('flags requested-checkpoint pressure only with enough checkpoints', () => {
    expect(cnpgCheckpointView({ source: 'pg_stat_checkpointer', timed: 2, requested: 8 }, 'primary')).toMatchObject({ total: 10, requestedShare: 0.8, pressure: true })
    expect(cnpgCheckpointView({ source: 'pg_stat_checkpointer', timed: 1, requested: 3 }, 'primary').pressure).toBe(false)
    expect(cnpgCheckpointView({ source: 'pg_stat_bgwriter', timed: 5 }, 'primary').requestedShare).toBeUndefined()
  })
  it('shows restartpoints for standbys on PostgreSQL 17+, never from pg_stat_bgwriter', () => {
    expect(cnpgCheckpointView({ source: 'pg_stat_checkpointer', restartpointsDone: 0 }, 'replica').showRestartpoints).toBe(true)
    expect(cnpgCheckpointView({ source: 'pg_stat_checkpointer', restartpointsDone: 0 }, 'primary').showRestartpoints).toBe(false)
    expect(cnpgCheckpointView({ source: 'pg_stat_bgwriter' }, 'replica').showRestartpoints).toBe(false)
  })
})

describe('cnpgDatabaseHealthRows', () => {
  it('joins per-database counters and ages by name and keeps unreported values unknown', () => {
    const rows = cnpgDatabaseHealthRows({
      state: 'ok',
      databases: [
        { database: 'app', xactCommit: 90, xactRollback: 10, tempFiles: 2, tempBytes: 4096 },
        { database: 'idle', xactCommit: 0, xactRollback: 0 },
        { database: 'noroll', xactCommit: 5 },
      ],
      xidAge: [{ database: 'app', age: 100 }],
      mxidAge: [{ database: 'postgres', age: 7 }],
    })
    expect(rows.map((r) => r.database)).toEqual(['app', 'idle', 'noroll', 'postgres'])
    expect(rows[0]).toEqual({ database: 'app', rollbackRatio: 0.1, tempFiles: 2, tempBytes: 4096, xidAge: 100 })
    expect(rows[1].rollbackRatio).toBeUndefined()
    expect(rows[2].rollbackRatio).toBeUndefined()
    expect(rows[3]).toEqual({ database: 'postgres', mxidAge: 7 })
  })
})

describe('standby cards', () => {
  const standby = (status: Partial<CNPGRuntimeInstance['status']>) => ({ pod: 'pg-2', role: 'replica', status: { state: 'ok', ...status } }) as unknown as CNPGRuntimeInstance
  const rep = { applicationName: 'pg-2', state: 'streaming', syncState: 'async' } as CNPGRuntimeReplication

  it('rates replay delay on the Replication fact scale', () => {
    expect(cnpgStandbyBacklogTone(0, 2)).toBe('healthy')
    expect(cnpgStandbyBacklogTone(0, 8)).toBe('degraded')
    expect(cnpgStandbyBacklogTone(0, 72)).toBe('unhealthy')
    expect(cnpgStandbyBacklogTone(2 * 1024 ** 3, 0)).toBe('unhealthy')
    expect(cnpgStandbyBacklogTone(undefined, 72)).toBe('unknown')
  })

  it('leads with a paused replay, keeping the streaming state secondary', () => {
    expect(cnpgStandbyHeadline(standby({ roleDetail: 'replayPaused' }), rep, 'healthy', { fenced: false, primaryRead: true })).toEqual({
      text: 'replay paused',
      tone: 'degraded',
      secondary: 'streaming · async',
    })
    expect(cnpgStandbyHeadline(standby({ replayPaused: true }), rep, 'unhealthy', { fenced: false, primaryRead: true }).tone).toBe('unhealthy')
    expect(cnpgStandbyHeadline(standby({ roleDetail: 'streaming' }), rep, 'healthy', { fenced: false, primaryRead: true })).toEqual({ text: 'streaming · async', tone: 'healthy' })
  })
})
