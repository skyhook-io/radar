import { describe, expect, it } from 'vitest'
import { cnpgCheckpointView, cnpgDatabaseHealthRows } from './runtimeModel'

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
