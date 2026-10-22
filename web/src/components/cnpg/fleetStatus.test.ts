import { describe, expect, it } from 'vitest'
import { buildCNPGFleet, type CNPGFleetRow, type CNPGProblem } from '@skyhook-io/k8s-ui'
import { cnpgInstancePillLabel, cnpgPillsToShow, cnpgRowStatus } from './fleetStatus'

const problem = (severity: CNPGProblem['severity'], title: string) => ({ id: title, severity, title }) as CNPGProblem
const row = (over: Partial<CNPGFleetRow>) =>
  ({ attention: false, problems: [], controllerStatus: { text: 'Cluster in healthy state', level: 'healthy' }, ...over }) as CNPGFleetRow

describe('cnpgRowStatus', () => {
  it('leads with the most severe problem and counts the rest', () => {
    expect(cnpgRowStatus(row({ attention: true, problems: [problem('warning', 'Backup failed: disk'), problem('critical', 'WAL archiving failing')] }))).toEqual({
      tone: 'unhealthy',
      label: 'Critical: WAL archiving failing (+1 more)',
    })
    expect(cnpgRowStatus(row({ attention: true, problems: [problem('warning', 'Backup failed: disk'), problem('posture', 'No backup schedule')] }))).toEqual({
      tone: 'degraded',
      label: 'Needs attention: Backup failed: disk (+1 more)',
    })
  })
  it('says no problems only with a readable phase, never healthy when unknown', () => {
    expect(cnpgRowStatus(row({}))).toEqual({ tone: 'healthy', label: 'No concerns detected in what Radar read · CNPG phase: Cluster in healthy state' })
    const unknown = cnpgRowStatus(row({ controllerStatus: { text: '', level: 'unknown' } }))
    expect(unknown).toEqual({ tone: 'unknown', label: 'Status unknown · CNPG phase: not reported' })
    expect(unknown.label).not.toContain('No concerns')
    expect(cnpgRowStatus(row({ controllerStatus: { text: 'Upgrading cluster', level: 'degraded' } })).tone).toBe('degraded')
  })
})

describe('cnpgInstancePillLabel', () => {
  it('names a replica as a standby too', () => {
    expect(cnpgInstancePillLabel({ name: 'pg-wal-failing-1', role: 'replica', ready: false })).toBe('pg-wal-failing-1 · replica (standby) · Pod not ready')
    expect(cnpgInstancePillLabel({ name: 'pg-1', role: 'primary', ready: null })).toBe('pg-1 · primary · Pod readiness unknown')
  })
})

describe('cnpgPillsToShow', () => {
  it('shows every pill up to the bound, and otherwise leaves room for "+N"', () => {
    expect(cnpgPillsToShow([1, 2, 3, 4, 5], 5)).toEqual({ shown: [1, 2, 3, 4, 5], hidden: [] })
    expect(cnpgPillsToShow([1, 2, 3, 4, 5, 6, 7], 5)).toEqual({ shown: [1, 2, 3, 4], hidden: [5, 6, 7] })
  })
})

it('gives a destination-blocked schedule an amber fleet status', () => {
  const fleet = buildCNPGFleet({ installed: true, context: 'test', namespaces: null, coverage: { clusters: { state: 'full' }, pods: { state: 'full' }, scheduledBackups: { state: 'full' } }, objects: { clusters: [{ apiVersion: 'postgresql.cnpg.io/v1', kind: 'Cluster', metadata: { name: 'payments', namespace: 'db' }, spec: { instances: 1 }, status: { phase: 'Cluster in healthy state', readyInstances: 1 } }], scheduledBackups: [{ metadata: { name: 'payments-nightly', namespace: 'db' }, spec: { cluster: { name: 'payments' } } }] }, issues: [{ id: 'blocked-schedule', reason: 'CNPGScheduleDestinationMissing', severity: 'warning', kind: 'ScheduledBackup', group: 'postgresql.cnpg.io', namespace: 'db', name: 'payments-nightly', message: 'Backup schedule payments-nightly cannot run: no backup destination' }], audit: [], backupsOmitted: 0 })
  expect(cnpgRowStatus(fleet.rows[0])).toMatchObject({ tone: 'degraded', label: 'Needs attention: Backup schedule payments-nightly cannot run: no backup destination' })
})
