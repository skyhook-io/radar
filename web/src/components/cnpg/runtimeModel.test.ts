import { describe, expect, it } from 'vitest'
import type { CNPGRuntimeInstance, CNPGRuntimeReplication } from '../../api/cnpg'
import { cnpgCheckpointView, cnpgDatabaseHealthRows, cnpgStandbyBacklogTone, cnpgPickedInstance, cnpgSessionAggregatesGap, cnpgStandbyHeadline, cnpgTransactionRates } from './runtimeModel'

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

describe('cnpgTransactionRates', () => {
  const p = (value: number, at: number, stale = false) => ({ value, at, stale })
  it('prefers a current Prometheus rate, with its time', () => {
    expect(cnpgTransactionRates({ commits: p(12.34, 100), rollbacks: p(0.5, 100) }, { commits: 3 }, 'primary only')).toEqual({
      source: 'prometheus',
      commits: '12.3',
      rollbacks: '0.5',
      at: 100,
    })
  })
  it('never shows a stopped Prometheus series as current', () => {
    expect(cnpgTransactionRates({ commits: p(12, 100, true), rollbacks: undefined }, { commits: 3 }, 'primary only')).toMatchObject({ source: 'sampled', commits: '3.0' })
    expect(cnpgTransactionRates({ commits: p(12, 100, true), rollbacks: undefined }, null, 'primary only')).toEqual({
      source: 'none',
      commits: 'no recent sample',
      rollbacks: 'no recent sample',
      at: 100,
    })
  })
  it('shows Prometheus rates without exporter access, and says why when there is neither', () => {
    expect(cnpgTransactionRates({ commits: p(4, 1), rollbacks: p(0, 1) }, null, 'needs get pods/proxy')).toMatchObject({ source: 'prometheus', commits: '4.0' })
    expect(cnpgTransactionRates({ commits: undefined, rollbacks: undefined }, null, 'needs get pods/proxy')).toMatchObject({ source: 'none', commits: 'needs get pods/proxy' })
  })
})

describe('Sessions instance and aggregates', () => {
  const inst = (pod: string, role: string, metrics: Record<string, unknown> = { state: 'ok', sessionsTotal: 3 }) => ({ pod, role, status: { state: 'ok' }, metrics }) as unknown as CNPGRuntimeInstance
  it('keeps standbys pickable when no primary is reported', () => {
    const standbys = [inst('pg-2', 'replica'), inst('pg-3', 'replica')]
    expect(cnpgPickedInstance(standbys, null)?.pod).toBe('pg-2')
    expect(cnpgPickedInstance([inst('pg-1', 'primary'), ...standbys], null)?.pod).toBe('pg-1')
    expect(cnpgPickedInstance([inst('pg-1', 'primary'), ...standbys], 'pg-3')?.pod).toBe('pg-3')
  })
  it('counts the aggregates as present only when sessions were measured', () => {
    expect(cnpgSessionAggregatesGap(inst('pg-1', 'primary'))).toBeUndefined()
    expect(cnpgSessionAggregatesGap(inst('pg-1', 'primary', { state: 'ok' }))).toBe('the metrics exporter on pg-1 reported no session counts')
    expect(cnpgSessionAggregatesGap(inst('pg-1', 'primary', { state: 'unreachable', error: 'x' }))).toBe('the metrics exporter on pg-1 did not answer (x)')
  })
})
