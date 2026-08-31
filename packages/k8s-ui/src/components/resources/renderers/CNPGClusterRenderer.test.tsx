import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { CNPGClusterRenderer } from './CNPGClusterRenderer'
import { getCNPGClusterStatus } from '../resource-utils-cnpg'

const cluster = (phase: string, readyInstances: number, extra: any = {}) => ({
  apiVersion: 'postgresql.cnpg.io/v1',
  kind: 'Cluster',
  metadata: { name: 'pg', namespace: 'db' },
  spec: { instances: 3 },
  status: { phase, readyInstances, ...extra },
})

const html = (resource: any) => renderToString(<CNPGClusterRenderer data={resource} />)

describe('CNPGClusterRenderer — the drawer must not contradict the badge', () => {
  it('does not raise Degraded for a shortfall the phase already explains', () => {
    // Mid-switchover at 2/3 is the operation, not a fault. The badge says
    // "Switchover"; the drawer used to say "Degraded Cluster" beside it.
    const switching = cluster('Switchover in progress', 2)
    expect(getCNPGClusterStatus(switching).text).toBe('Switchover')
    expect(html(switching)).not.toContain('Degraded Cluster')
  })

  it.each([
    ['Upgrading cluster', 'transient'],
    ['Waiting for user action', 'attention'],
    ['Failing over', 'failing'],
    ['Cluster is unrecoverable and needs manual intervention', 'terminal'],
  ])('suppresses the Degraded banner during %s (%s)', (phase) => {
    expect(html(cluster(phase, 2))).not.toContain('Degraded Cluster')
  })

  it('still raises Degraded when the phase does NOT explain the shortfall', () => {
    // The regression guard for the fix: a settled cluster missing an instance
    // is a genuine problem, and both surfaces must say so.
    const settled = cluster('Cluster in healthy state', 2)
    expect(getCNPGClusterStatus(settled).text).toBe('Degraded')
    expect(html(settled)).toContain('Degraded Cluster')
  })

  it('still raises Cluster Down at zero ready, whatever the phase', () => {
    // No phase excuses a database with nothing serving.
    expect(html(cluster('Upgrading cluster', 0))).toContain('Cluster Down')
  })

  it('renders a failed last backup at the same tier as the badge and detector', () => {
    // Three surfaces, one severity: badge `degraded`, Go SeverityWarning, and
    // the banner. An error banner made the drawer the loudest of the three.
    const failed = cluster('Cluster in healthy state', 3, {
      conditions: [{ type: 'LastBackupSucceeded', status: 'False', reason: 'BackupFailed', message: 'upload rejected' }],
    })
    const out = html(failed)
    expect(out).toContain('Last backup failed')
    expect(getCNPGClusterStatus(failed).level).toBe('degraded')
    // AlertBanner keys its tone off the variant; the error tone must be absent
    // for this banner while the warning tone is present.
    const banner = out.slice(out.indexOf('Last backup failed') - 400, out.indexOf('Last backup failed'))
    expect(banner).not.toMatch(/status-unhealthy|text-red-/)
  })
})

describe('the WAL banner must not claim nothing else looks wrong when something does', () => {
  const walFailing = (phase: string, ready: number) => cluster(phase, ready, {
    conditions: [{ type: 'ContinuousArchiving', status: 'False', reason: 'ContinuousArchivingFailing' }],
  })
  const CLAIM = 'otherwise serving normally'

  it('makes the claim on a genuinely healthy cluster', () => {
    expect(html(walFailing('Cluster in healthy state', 3))).toContain(CLAIM)
  })

  it('drops it mid-switchover, where a Switchover banner sits right above', () => {
    // Regression guard for a sibling the phase gate itself introduced: this
    // clause used to be suppressed by isDegraded, which a phase-explained
    // shortfall no longer sets.
    const out = html(walFailing('Switchover in progress', 2))
    expect(out).toContain('Switchover in Progress')
    expect(out).not.toContain(CLAIM)
  })

  it.each(['Waiting for user action', 'Upgrading cluster', 'Failing over'])('drops it during %s', (phase) => {
    expect(html(walFailing(phase, 2))).not.toContain(CLAIM)
  })
})

describe('recovery points must be readable as ages, not raw machine timestamps', () => {
  const withBackup = (lastSuccessful: string) => ({
    apiVersion: 'postgresql.cnpg.io/v1',
    kind: 'Cluster',
    metadata: { name: 'pg', namespace: 'db' },
    spec: { instances: 2, backup: { barmanObjectStore: { destinationPath: 's3://bucket' } } },
    status: {
      phase: 'Cluster in healthy state',
      readyInstances: 2,
      lastSuccessfulBackup: lastSuccessful,
      firstRecoverabilityPoint: lastSuccessful,
    },
  })

  it('states how old the last successful backup is', () => {
    const old = new Date(Date.now() - 200 * 24 * 60 * 60 * 1000).toISOString()
    const html = renderToString(<CNPGClusterRenderer data={withBackup(old)} />)
    // The age is the whole point: a raw ISO string requires the reader to do
    // date arithmetic to notice their newest backup is 200 days old.
    expect(html).toContain('200d ago')
    // And the ISO form with its milliseconds must not survive as the display value.
    expect(html).not.toContain(old)
  })

  it('keeps the absolute timestamp beside the age', () => {
    // Age alone is the wrong unit here: "1d ago" next to "1d ago" is the same
    // string for events a week apart in a failure history.
    const at = '2026-01-30T09:40:37.000Z'
    const html = renderToString(<CNPGClusterRenderer data={withBackup(at)} />)
    expect(html).toContain('2026-01-30 09:40:37 UTC')
  })
})

describe('CNPGClusterRenderer — the replica-cluster role tracks live state, not stanza presence', () => {
  const withReplica = (replica: any, name = 'pg-eu') => ({
    apiVersion: 'postgresql.cnpg.io/v1',
    kind: 'Cluster',
    metadata: { name, namespace: 'db' },
    spec: { instances: 3, replica },
    status: { phase: 'Cluster in healthy state', readyInstances: 3 },
  })

  it('shows the Replica Cluster section for a genuine replica (replica mode enabled)', () => {
    const out = renderToString(<CNPGClusterRenderer data={withReplica({ source: 'pg-us', enabled: true })} />)
    expect(out).toContain('Replica Cluster')
    expect(out).toContain('Role')
    expect(out).toContain('pg-us')
  })

  it('shows Replica for a distributed-topology cluster whose primary is elsewhere', () => {
    const out = renderToString(
      <CNPGClusterRenderer data={withReplica({ source: 'pg-us', primary: 'pg-us', self: 'pg-eu' })} />,
    )
    expect(out).toContain('Replica Cluster')
  })

  it('drops the Replica label once a distributed replica is promoted, though the stanza remains', () => {
    // Promotion sets replica.primary to this cluster; the stanza is not removed.
    const out = renderToString(
      <CNPGClusterRenderer data={withReplica({ source: 'pg-us', primary: 'pg-eu', self: 'pg-eu' })} />,
    )
    expect(out).not.toContain('Replica Cluster')
  })

  it('drops the Replica label once a standalone replica is promoted (replica.enabled off)', () => {
    const out = renderToString(<CNPGClusterRenderer data={withReplica({ source: 'pg-us', enabled: false })} />)
    expect(out).not.toContain('Replica Cluster')
  })

  it('a normal standalone cluster shows no Replica Cluster section', () => {
    const out = renderToString(<CNPGClusterRenderer data={cluster('Cluster in healthy state', 3)} />)
    expect(out).not.toContain('Replica Cluster')
  })
})
