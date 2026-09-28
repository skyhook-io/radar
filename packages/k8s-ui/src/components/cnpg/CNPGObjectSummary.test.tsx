import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { CNPGBackupSummary, CNPGScheduledBackupSummary } from './CNPGBackupSummary'
import { CNPGObjectStoreSummary } from './CNPGObjectStoreSummary'
import { CNPGDatabaseSummary } from './CNPGDeclarativeSummary'
import { CNPGPoolerSummary } from './CNPGPoolerSummary'
import { CNPGImageCatalogSummary } from './CNPGImageCatalogSummary'
import { CNPG_WORKSPACE_KEYS, type CNPGWorkspaceKey, type CNPGWorkspaceResponse } from './workspace'

const PG = 'postgresql.cnpg.io/v1'
const PLUGIN = 'barman-cloud.cloudnative-pg.io'
const nav = () => {}

function ws(objects: Partial<Record<CNPGWorkspaceKey, any[]>>, over: Partial<CNPGWorkspaceResponse> = {}): CNPGWorkspaceResponse {
  const coverage: CNPGWorkspaceResponse['coverage'] = {}
  for (const k of CNPG_WORKSPACE_KEYS) coverage[k] = { state: 'full' }
  return {
    installed: true,
    context: 'c',
    namespaces: ['pg'],
    coverage: { ...coverage, ...(over.coverage ?? {}) },
    objects,
    issues: over.issues ?? [],
    audit: [],
    backupsOmitted: over.backupsOmitted ?? 0,
  }
}

function text(html: string): string {
  return html.replace(/<[^>]+>/g, '').replace(/&#x27;/g, "'").replace(/&quot;/g, '"').replace(/&amp;/g, '&')
}

const mainCluster = {
  apiVersion: PG,
  kind: 'Cluster',
  metadata: { name: 'main', namespace: 'pg' },
  spec: { plugins: [{ name: PLUGIN, parameters: { barmanObjectName: 'store' } }], managed: { roles: [{ name: 'reporting' }] } },
  status: { conditions: [{ type: 'ContinuousArchiving', status: 'False', message: 'upload failed' }] },
}

describe('CNPGBackupSummary', () => {
  const b = {
    apiVersion: PG,
    kind: 'Backup',
    metadata: { name: 'main-20260901', namespace: 'pg', labels: { 'cnpg.io/scheduled-backup': 'nightly' } },
    spec: { cluster: { name: 'main' }, method: 'plugin', pluginConfiguration: { name: PLUGIN } },
    status: {
      phase: 'failed',
      startedAt: '2026-09-01T00:00:00Z',
      stoppedAt: '2026-09-01T00:02:30Z',
      instanceID: { podName: 'main-2' },
      error: 'can not upload',
    },
  }

  it('shows the outcome and relationships from the same status fields', () => {
    const html = renderToString(<CNPGBackupSummary resource={b} workspace={ws({ clusters: [mainCluster] })} onNavigate={nav} />)
    const t = text(html)
    expect(t).toContain('Failed')
    expect(t).toContain('took 2m 30s')
    expect(t).toContain('main-2')
    expect(t).toContain('can not upload')
    expect(t).toContain('ScheduledBackup nightly')
    expect(t).toContain('ObjectStore store')
    expect(t).not.toContain('On demand')
  })

  it('shows its own issues on top', () => {
    const issues = [
      { id: 'i1', severity: 'critical' as const, kind: 'Backup', group: 'postgresql.cnpg.io', namespace: 'pg', name: 'main-20260901', reason: 'CNPGBackupFailed', message: 'Backup failed' },
      { id: 'i2', severity: 'critical' as const, kind: 'Backup', group: 'velero.io', namespace: 'pg', name: 'main-20260901', reason: 'VeleroBackupFailed', message: 'Velero backup failed' },
    ]
    const t = text(renderToString(<CNPGBackupSummary resource={b} workspace={ws({ clusters: [mainCluster] }, { issues })} onNavigate={nav} />))
    expect(t).toContain('Backup failed')
    expect(t).not.toContain('Velero backup failed')
  })
})

describe('CNPGScheduledBackupSummary', () => {
  it('lists owned runs and notes omitted history', () => {
    const sched = { apiVersion: PG, kind: 'ScheduledBackup', metadata: { name: 'nightly', namespace: 'pg' }, spec: { cluster: { name: 'main' }, schedule: '0 0 0 * * *' } }
    const run = { apiVersion: PG, kind: 'Backup', metadata: { name: 'run-1', namespace: 'pg', labels: { 'cnpg.io/scheduled-backup': 'nightly' } }, status: { phase: 'completed', startedAt: '2026-09-01T00:00:00Z' } }
    const t = text(renderToString(<CNPGScheduledBackupSummary resource={sched} workspace={ws({ backups: [run] }, { backupsOmitted: 3 })} onNavigate={nav} />))
    expect(t).toContain('run-1')
    expect(t).toContain('Completed')
    expect(t).toContain('Backups older than 7 days are not listed')
  })

  it('says when Backups are not readable instead of listing none', () => {
    const sched = { apiVersion: PG, kind: 'ScheduledBackup', metadata: { name: 'nightly', namespace: 'pg' }, spec: { cluster: { name: 'main' } } }
    const t = text(renderToString(<CNPGScheduledBackupSummary resource={sched} workspace={ws({}, { coverage: { backups: { state: 'denied' } } })} />))
    expect(t).toContain('No access to Backups')
    expect(t).not.toContain('No Backups from this schedule')
  })
})

describe('CNPGObjectStoreSummary', () => {
  const store = {
    apiVersion: 'barmancloud.cnpg.io/v1',
    kind: 'ObjectStore',
    metadata: { name: 'store', namespace: 'pg' },
    spec: {
      configuration: {
        destinationPath: 's3://bucket/pg',
        s3Credentials: { accessKeyId: { name: 's3-creds', key: 'ACCESS_KEY_ID' }, secretAccessKey: { name: 's3-creds', key: 'SECRET' } },
      },
      retentionPolicy: '30d',
    },
    status: {
      serverRecoveryWindow: {
        main: { firstRecoverabilityPoint: '2026-09-01T00:00:00Z', lastSuccessfulBackupTime: '2026-09-02T00:00:00Z', lastFailedBackupTime: '2026-09-03T00:00:00Z' },
      },
    },
  }

  it('labels upload health as inferred and explains the stalled window', () => {
    const t = text(renderToString(<CNPGObjectStoreSummary resource={store} workspace={ws({ clusters: [mainCluster] })} onNavigate={nav} />))
    expect(t).toContain('inferred')
    expect(t).toContain("Inferred from 1 cluster's WAL archiving and backup results — ObjectStore has no health status")
    expect(t).toContain('Uploads failing')
    expect(t).toContain('The window is still restorable up to the last successful backup; it stops advancing while uploads fail.')
    expect(t).toContain('s3-creds')
    expect(t).toContain('s3://bucket/pg')
  })

  it('never renders credential keys or Secret contents', () => {
    const html = renderToString(<CNPGObjectStoreSummary resource={store} workspace={ws({ clusters: [mainCluster] })} onNavigate={nav} />)
    expect(html).not.toContain('ACCESS_KEY_ID')
    expect(html).not.toContain('SECRET')
  })

  it('says when no visible cluster uses the store', () => {
    const t = text(renderToString(<CNPGObjectStoreSummary resource={store} workspace={ws({ clusters: [] })} />))
    expect(t).toContain('No visible cluster uses this store')
    expect(t).not.toContain('Inferred from')
  })
})

describe('CNPGDatabaseSummary', () => {
  const db = (status: any) => ({
    apiVersion: PG,
    kind: 'Database',
    metadata: { name: 'app-db', namespace: 'pg', generation: 2 },
    spec: { cluster: { name: 'main' }, name: 'app', owner: 'app', databaseReclaimPolicy: 'delete' },
    status,
  })

  it('reports an unreconciled database as pending, not failed', () => {
    const t = text(renderToString(<CNPGDatabaseSummary resource={db({})} workspace={ws({ clusters: [mainCluster] })} onNavigate={nav} />))
    expect(t).toContain('Pending')
    expect(t).not.toContain('Not applied')
    expect(t).toContain('drops it from PostgreSQL')
    expect(t).toContain('Applied directly (no GitOps owner label)')
  })

  it('reports a failure and the missing managed role beside it', () => {
    const t = text(
      renderToString(
        <CNPGDatabaseSummary
          resource={db({ applied: false, observedGeneration: 2, message: 'role "app" does not exist' })}
          workspace={ws({ clusters: [mainCluster] })}
          onNavigate={nav}
        />,
      ),
    )
    expect(t).toContain('Not applied')
    expect(t).toContain('“app” is not among main\'s managed roles')
  })
})

describe('CNPGPoolerSummary', () => {
  it('reports unknown scheduled count and unmeasured pressure', () => {
    const pooler = { apiVersion: PG, kind: 'Pooler', metadata: { name: 'main-rw', namespace: 'pg' }, spec: { cluster: { name: 'main' }, type: 'rw', instances: 2 } }
    const t = text(renderToString(<CNPGPoolerSummary resource={pooler} workspace={ws({})} onNavigate={nav} />))
    expect(t).toContain('Scheduled count not reported')
    expect(t).toContain('Not measured')
    expect(t).toContain('main-rw')
  })
})

describe('CNPGImageCatalogSummary', () => {
  it('lists users and flags a major the catalog lacks', () => {
    const catalog = { apiVersion: PG, kind: 'ClusterImageCatalog', metadata: { name: 'pg' }, spec: { images: [{ major: 16, image: 'ghcr.io/cnpg/postgresql:16' }] } }
    const clusters = [
      { apiVersion: PG, kind: 'Cluster', metadata: { name: 'a', namespace: 'x' }, spec: { imageCatalogRef: { kind: 'ClusterImageCatalog', name: 'pg', major: 16 } } },
      { apiVersion: PG, kind: 'Cluster', metadata: { name: 'b', namespace: 'y' }, spec: { imageCatalogRef: { kind: 'ClusterImageCatalog', name: 'pg', major: 17 } } },
    ]
    const t = text(renderToString(<CNPGImageCatalogSummary resource={catalog} workspace={ws({ clusters })} onNavigate={nav} />))
    expect(t).toContain('x/a')
    expect(t).toContain('Requests PostgreSQL 17 · not in this catalog')
    expect(t).toContain('clusters in namespaces you cannot read are not listed')
  })
})
