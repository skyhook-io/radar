import { describe, expect, it } from 'vitest'
import type { CNPGRuntimeInstance, CNPGRuntimeReplication } from '../../api/cnpg'
import type { CNPGSessionsResponse } from '../../api/cnpg-sessions'
import { cnpgCheckpointView, cnpgDatabaseHealthRows, cnpgNoStandbyText, cnpgIdAge, cnpgStandbyBacklogTone, cnpgPickedInstance, cnpgSessionAggregatesGap, cnpgSessionsCardShowsConnections, cnpgStandbyHeadline, cnpgTransactionRates } from './runtimeModel'

describe('cnpgCheckpointView', () => {
  it('flags requested-checkpoint pressure only with enough checkpoints', () => {
    expect(cnpgCheckpointView({ source: 'pg_stat_checkpointer', timed: 2, requested: 8 }, 'primary')).toMatchObject({ total: 10, requestedShare: 0.8, pressure: true })
    expect(cnpgCheckpointView({ source: 'pg_stat_checkpointer', timed: 1, requested: 3 }, 'primary').pressure).toBe(false)
    expect(cnpgCheckpointView({ source: 'pg_stat_checkpointer', timed: 1, requested: 2 }, 'primary').share).toBeUndefined()
    expect(cnpgCheckpointView({ source: 'pg_stat_checkpointer', timed: 2, requested: 8 }, 'primary').share).toBe('80 %')
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
    expect(rows[1].noTransactions).toBe(true)
    expect(rows[2].rollbackRatio).toBeUndefined()
    expect(rows[2].noTransactions).toBeUndefined()
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

  it('says a fenced instance stopped only when the instance manager reports it', () => {
    const fenced = { fenced: true, primaryRead: true }
    expect(cnpgStandbyHeadline(standby({ state: 'unreachable', error: 'instance is fenced: (PostgreSQL is not running on this instance)' }), undefined, 'unknown', fenced).text).toBe('fenced · PostgreSQL not running')
    expect(cnpgStandbyHeadline(standby({ state: 'denied' }), undefined, 'unknown', fenced).text).toBe('fenced · shutdown unverified')
    expect(cnpgStandbyHeadline(standby({ state: 'ok' }), undefined, 'unknown', fenced).text).toBe('fenced · PostgreSQL still answering')
  })

  it('says a paused standby is not connected when the primary has no row for it', () => {
    expect(cnpgStandbyHeadline(standby({ replayPaused: true }), undefined, 'unhealthy', { fenced: false, primaryRead: true })).toEqual({
      text: 'replay paused',
      tone: 'unhealthy',
      secondary: 'not connected to the primary',
    })
    expect(cnpgStandbyHeadline(standby({ replayPaused: true }), undefined, 'unknown', { fenced: false, primaryRead: false }).secondary).toBeUndefined()
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

describe('cnpgSessionsCardShowsConnections', () => {
  const inst = (metrics: Record<string, unknown>) => ({ pod: 'pg-1', role: 'primary', status: { state: 'ok' }, metrics }) as unknown as CNPGRuntimeInstance
  const exec = { pod: 'pg-1', state: 'ok', maxConnections: 100, superuserReservedConnections: 3, clientBackends: 91 } as unknown as CNPGSessionsResponse
  it('shows headroom once: in the card when it has a figure, otherwise in Blocking', () => {
    expect(cnpgSessionsCardShowsConnections(inst({ state: 'ok' }), exec)).toBe(true)
    expect(cnpgSessionsCardShowsConnections(inst({ state: 'ok', sessionsTotal: 5 }), undefined)).toBe(true)
    expect(cnpgSessionsCardShowsConnections(inst({ state: 'ok' }), undefined)).toBe(false)
    expect(cnpgSessionsCardShowsConnections(inst({ state: 'unreachable' }), exec)).toBe(false)
  })
})

describe('cnpgIdAge', () => {
  it('reads as a count with k / M / B and never shows a non-zero age as zero', () => {
    expect(cnpgIdAge(0)).toBe('0')
    expect(cnpgIdAge(812)).toBe('812')
    expect(cnpgIdAge(1_234)).toBe('1.2k')
    expect(cnpgIdAge(45_000)).toBe('45k')
    expect(cnpgIdAge(3_400_000)).toBe('3.4 M')
    expect(cnpgIdAge(1_100_000_000)).toBe('1.1 B')
    expect(cnpgIdAge(12)).not.toBe('0 M')
  })
})

describe('cnpgNoStandbyText', () => {
  it('calls a one-instance cluster single, a larger one short of standbys, and claims neither unread', () => {
    expect(cnpgNoStandbyText(1)).toBe('Single instance: no replica to fail over to.')
    expect(cnpgNoStandbyText(2)).toBe('No standby is running: spec.instances is 2, so one standby is expected. Nothing to fail over to until one joins.')
    expect(cnpgNoStandbyText(3)).toContain('2 standbys are expected')
    expect(cnpgNoStandbyText(undefined)).toBe('No standby is running.')
  })
})

it('keeps the measured connections in Sessions when only its row lists were capped', () => {
  const inst = { pod: 'pg-1', metrics: { state: 'partial', reason: 'sessions capped', sessionsTotal: 150 } } as CNPGRuntimeInstance
  expect(cnpgSessionAggregatesGap(inst)).toBeUndefined()
  expect(cnpgSessionsCardShowsConnections(inst, undefined)).toBe(true)
})
