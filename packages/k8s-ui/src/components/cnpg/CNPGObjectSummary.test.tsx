import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { CNPGBackupSummary, CNPGScheduledBackupSummary } from './CNPGBackupSummary'
import { CNPGObjectStoreSummary } from './CNPGObjectStoreSummary'
import { CNPGDatabaseSummary, CNPGPublicationSummary, CNPGSubscriptionSummary } from './CNPGDeclarativeSummary'
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
  return html
    .split(/<[^>]*>/)
    .join('')
    .split('&#x27;').join("'")
    .split('&quot;').join('"')
    .split('&amp;').join('&')
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
    expect(t).toContain("ObjectStore store · from the Cluster's current configuration")
    expect(t).not.toContain('On demand')
  })

  it('does not link a same-name schedule that replaced the one that took the backup', () => {
    const owned = { ...b, metadata: { ...b.metadata, ownerReferences: [{ apiVersion: PG, kind: 'ScheduledBackup', name: 'nightly', uid: 'old' }] } }
    const sched = { apiVersion: PG, kind: 'ScheduledBackup', metadata: { name: 'nightly', namespace: 'pg', uid: 'new' } }
    const t = text(renderToString(<CNPGBackupSummary resource={owned} workspace={ws({ clusters: [mainCluster], scheduledBackups: [sched] })} onNavigate={nav} />))
    expect(t).toContain('an earlier schedule of that name')
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
    expect(t).toContain('0 0 0 * * *')
    expect(t).toContain('seconds first')
    expect(t).not.toContain('Daily')
    expect(t).toContain('run-1')
    expect(t).toContain('Completed')
    expect(t).toContain('Backups older than 7 days are not listed')
  })

  it("shows the server's reading and next runs only for the schedule it read", () => {
    const sched = { apiVersion: PG, kind: 'ScheduledBackup', metadata: { name: 'nightly', namespace: 'pg' }, spec: { cluster: { name: 'main' }, schedule: '0 30 2 * * *' } }
    const preview = {
      schedule: '0 30 2 * * *',
      valid: true,
      description: 'every day at 02:30:00 UTC',
      nextRuns: ['2026-10-01T02:30:00Z', '2026-10-02T02:30:00Z', '2026-10-03T02:30:00Z'],
      basis: 'lastCheckTime' as const,
    }
    const t = text(renderToString(<CNPGScheduledBackupSummary resource={sched} workspace={ws({})} schedulePreview={preview} />))
    expect(t).toContain('every day at 02:30:00 UTC')
    expect(t).toContain('2026-10-01 02:30:00 UTC')
    expect(t).toContain('Calculated upcoming times')
    expect(t).toContain('Next run reported by the operatorNot reported')
    expect(t).toContain("operator's last check")
    const stale = text(renderToString(<CNPGScheduledBackupSummary resource={sched} workspace={ws({})} schedulePreview={{ ...preview, schedule: '0 0 0 * * *' }} />))
    expect(stale).not.toContain('every day at')
    const due = text(renderToString(<CNPGScheduledBackupSummary resource={sched} workspace={ws({})} schedulePreview={{ ...preview, runsImmediately: true }} />))
    expect(due).toContain('now (2026-10-01 02:30:00 UTC)')
    expect(due).toContain('runs one backup as soon as it sees this schedule')
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
    expect(t).toContain('A backup failed after the last recorded success.')
    expect(t).not.toContain('restorable')
    expect(t).toContain('s3-creds')
    expect(t).toContain('s3://bucket/pg')
  })

  it('never renders credential keys or Secret contents', () => {
    const html = renderToString(<CNPGObjectStoreSummary resource={store} workspace={ws({ clusters: [mainCluster] })} onNavigate={nav} />)
    expect(html).not.toContain('ACCESS_KEY_ID')
    expect(html).not.toContain('SECRET')
  })

  it('records a failure with no success without claiming a recovery point', () => {
    const failing = { ...store, status: { serverRecoveryWindow: { main: { lastFailedBackupTime: '2026-09-03T00:00:00Z' } } } }
    const t = text(renderToString(<CNPGObjectStoreSummary resource={failing} workspace={ws({ clusters: [mainCluster] })} />))
    expect(t).toContain('No successful backup recorded.')
    expect(t).not.toContain('restorable')
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
    expect(t).toContain('GitOps source not recorded')
    expect(t).not.toContain('Applied directly')
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
    expect(t).toContain('Pooler instance count not reported')
    expect(t).toContain('Not measured')
    expect(t).toContain('main-rw')
  })

  it('shows Deployment readiness, limits with PgBouncer defaults, observed pause and the Service path when live data is provided', () => {
    const pooler = {
      apiVersion: PG,
      kind: 'Pooler',
      metadata: { name: 'main-rw', namespace: 'pg' },
      spec: { cluster: { name: 'main' }, type: 'rw', instances: 2, pgbouncer: { paused: true, parameters: { max_client_conn: '200' } } },
      status: { instances: 2 },
    }
    const t = text(
      renderToString(
        <CNPGPoolerSummary
          resource={pooler}
          workspace={ws({})}
          onNavigate={nav}
          live={{
            deployment: { name: 'main-rw', state: 'ok', replicas: 2, readyReplicas: 1 },
            service: { name: 'main-rw', state: 'ok', type: 'ClusterIP', port: 5432 },
            pressure: { state: 'ok', pods: [{ pod: 'a', state: 'ok', pools: [{ database: 'app', user: 'app', clActive: 4, clWaiting: 2 }] }] },
            observed: { state: 'ok', pods: [{ pod: 'a', state: 'ok', paused: true }, { pod: 'b', state: 'ok', paused: false }] },
          }}
        />,
      ),
    )
    expect(t).toContain('from Deployment main-rw')
    expect(t).toContain('Pause requested')
    expect(t).toContain('1/2 ready')
    expect(t).toContain('Pause state')
    expect(t).toContain('Observed: Paused on 1 of 2 PgBouncers')
    expect(t).toContain('PgBouncer uses 20')
    expect(t).toContain('200')
    expect(t).toContain('main-rw')
    expect(t).toContain('app/app')
    expect(t).not.toContain('Not measured')
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

  it('keeps known users when cluster coverage is partial', () => {
    const catalog = { apiVersion: PG, kind: 'ClusterImageCatalog', metadata: { name: 'pg' }, spec: { images: [{ major: 16, image: 'img:16' }] } }
    const clusters = [{ apiVersion: PG, kind: 'Cluster', metadata: { name: 'a', namespace: 'x' }, spec: { imageCatalogRef: { kind: 'ClusterImageCatalog', name: 'pg', major: 16 } } }]
    const t = text(
      renderToString(
        <CNPGImageCatalogSummary resource={catalog} workspace={ws({ clusters }, { coverage: { clusters: { state: 'partial', deniedNamespaces: ['y'] } } })} />,
      ),
    )
    expect(t).toContain('x/a')
    expect(t).toContain('Only clusters in namespaces you can read are listed.')
    expect(t).not.toContain('No access to Clusters')
  })

  it('reserves the unavailable text for denied coverage', () => {
    const catalog = { apiVersion: PG, kind: 'ClusterImageCatalog', metadata: { name: 'pg' }, spec: {} }
    const t = text(renderToString(<CNPGImageCatalogSummary resource={catalog} workspace={ws({}, { coverage: { clusters: { state: 'denied' } } })} />))
    expect(t).toContain('No access to Clusters')
  })
})

describe('logical replication summaries', () => {
  const src = { apiVersion: PG, kind: 'Cluster', metadata: { name: 'src', namespace: 'pg' }, spec: { instances: 2 } }
  const dst = { apiVersion: PG, kind: 'Cluster', metadata: { name: 'dst', namespace: 'pg' }, spec: { instances: 1, externalClusters: [{ name: 'src', connectionParameters: { host: 'src-rw', dbname: 'app' } }] } }
  const pub = { apiVersion: PG, kind: 'Publication', metadata: { name: 'orders-pub', namespace: 'pg' }, spec: { cluster: { name: 'src' }, name: 'orders_pub', dbname: 'app', target: { allTables: true } }, status: { applied: true } }
  const sub = { apiVersion: PG, kind: 'Subscription', metadata: { name: 'orders-sub', namespace: 'pg' }, spec: { cluster: { name: 'dst' }, name: 'orders_sub', dbname: 'app', publicationName: 'orders_pub', externalClusterName: 'src' }, status: { applied: true } }
  const w = ws({ clusters: [src, dst], publications: [pub], subscriptions: [sub] })

  it('shows the subscription path with the slot unread and the failover verdict', () => {
    const t = text(renderToString(<CNPGSubscriptionSummary resource={sub} workspace={w} onNavigate={nav} />))
    expect(t).toContain('Replication path')
    expect(t).toContain('orders_pub')
    expect(t).toContain('Slot orders_sub: not read')
    expect(t).toContain('Lost on failover')
    expect(t).toContain('no pg_stat_subscription query')
  })

  it('never says no Publication declares it when Publications are unreadable', () => {
    const denied = ws({ clusters: [src, dst], subscriptions: [sub] }, { coverage: { publications: { state: 'denied' } } })
    const t = text(renderToString(<CNPGSubscriptionSummary resource={sub} workspace={denied} onNavigate={nav} />))
    expect(t).toContain('Unknown: No access to Publications in pg')
    expect(t).not.toContain('No Publication object declares it')
  })

  it('lists the subscribers of a publication', () => {
    const t = text(renderToString(<CNPGPublicationSummary resource={pub} workspace={w} onNavigate={nav} />))
    expect(t).toContain('Subscribers')
    expect(t).toContain('orders_sub')
  })
})

it('shows the stale declaration as pending in its drawer', () => {
  const resource = { apiVersion: PG, kind: 'Database', metadata: { name: 'app', namespace: 'pg', generation: 3 }, spec: { name: 'app' }, status: { applied: true, observedGeneration: 2 } }
  const t = text(renderToString(<CNPGDatabaseSummary resource={resource} workspace={ws({})} />))
  expect(t).toContain('Pending · awaiting the operator for the current spec')
  expect(t).toContain('AppliedPending')
})

it('keeps Deployment readiness beside the pause request even when no Pods are ready', () => {
  const resource = { metadata: { name: 'p' }, spec: { pgbouncer: { paused: true } } }
  const t = text(renderToString(<CNPGPoolerSummary resource={resource} workspace={ws({})} live={{ deployment: { name: 'p', state: 'ok', replicas: 2, readyReplicas: 0 } }} />))
  expect(t).toContain('0/2 ready')
  expect(t).toContain('Pause requested')
  expect(t).not.toContain('Observed: Paused')
})

it('puts the pending Pooler cause under readiness and names the Pod whose metric read failed', () => {
  const html = renderToString(<CNPGPoolerSummary resource={{ metadata: { name: 'pooler', namespace: 'pg' } }} workspace={ws({})} onNavigate={nav} live={{ deployment: { name: 'pooler', state: 'ok', replicas: 1, readyReplicas: 0 }, pressure: { state: 'ok', pods: [{ pod: 'pooler-pod', state: 'unreachable', error: 'address not allowed', schedulingReason: 'Unschedulable: insufficient cpu' }] } }} />)
  const t = text(html)
  expect(t).toContain('0/1 ready')
  expect(t).toContain('pooler-pod cannot be scheduled: insufficient cpu.')
  expect(t).toContain('Not measured: PgBouncer did not answer')
  expect(t).toContain('pooler-pod')
  expect(t).toContain('Measurement details')
  expect(t).toContain('address not allowed')
  expect(t.indexOf('cannot be scheduled')).toBeLessThan(t.indexOf('Connections'))
  expect(t.indexOf('address not allowed')).toBeGreaterThan(t.indexOf('Connections'))
  expect(html).toContain('button')
})

it('never calls an incomplete empty Pooler read idle and names the unread Pod', () => {
  const t = text(renderToString(<CNPGPoolerSummary resource={{}} workspace={ws({})} live={{ pressure: { state: 'ok', pods: [{ pod: 'a', state: 'ok', pools: [] }, { pod: 'b', state: 'unreachable', error: 'timeout' }] } }} />))
  expect(t).toContain('No pools seen in what was read')
  expect(t).toContain('b: not read (timeout)')
  expect(t).not.toContain('Idle:')
})

it('leads Pooler observations with the scheduling cause and labels the operator count', () => {
  const pod = { pod: 'orders-pooler-pod', state: 'unreachable', reason: 'PgBouncer has not started (Pod cannot be scheduled)', schedulingReason: 'Unschedulable: 0/2 nodes are available: 2 Too many pods. preemption: no victims.' }
  const html = renderToString(<CNPGPoolerSummary resource={{ metadata: { name: 'p', namespace: 'db' }, spec: { instances: 1 }, status: { instances: 1 } }} workspace={ws({})} live={{ pressure: { state: 'ok', pods: [pod] }, observed: { state: 'ok', pods: [{ ...pod, error: pod.reason }] } }} />)
  const t = text(html)
  expect(t).toContain('Pooler status reports 1 instance · 1 requested')
  expect(t).toContain('cannot be scheduled: both nodes have reached their Pod limit')
  expect(t).toContain('Not measured: PgBouncer has not started (Pod cannot be scheduled)')
  expect(t).toContain('1 not read: orders-pooler-pod (PgBouncer has not started (Pod cannot be scheduled))')
  expect(html).toContain('aria-expanded="false"')
  expect(t).toContain('preemption: no victims.')
})
