import { describe, expect, it } from 'vitest'
import {
  cnpgScheduleDestinationBlocker,
  appliedFact,
  backupDestination,
  backupsForScheduledBackup,
  clustersUsingCatalog,
  databaseForDeclaration,
  inferredObjectStoreHealth,
  isBackupFromSchedule,
  issuesForObject,
  missingManagedRole,
  objectStoreForBackup,
  relationUnavailable,
  replicationForDatabase,
  scheduledBackupOf,
  usersOfObjectStore,
} from './relations'

it('checks the destination for the schedule method and keeps unread targets unknown', () => {
  const cluster = { apiVersion: 'postgresql.cnpg.io/v1', kind: 'Cluster', metadata: { name: 'payments', namespace: 'pg' }, spec: { backup: { volumeSnapshot: {} }, plugins: [{ name: 'barman-cloud.cloudnative-pg.io', parameters: { barmanObjectName: 'store' } }] } }
  const schedule = { metadata: { namespace: 'pg' }, spec: { cluster: { name: 'payments' } } }
  expect(cnpgScheduleDestinationBlocker(schedule, [cluster])).toBe('No barmanObjectStore destination')
  expect(cnpgScheduleDestinationBlocker(schedule, [])).toBeNull()
  expect(cnpgScheduleDestinationBlocker({ ...schedule, spec: { ...schedule.spec, method: 'volumeSnapshot' } }, [cluster])).toBeNull()
  expect(cnpgScheduleDestinationBlocker({ ...schedule, spec: { ...schedule.spec, method: 'plugin', pluginConfiguration: { name: 'barman-cloud.cloudnative-pg.io' } } }, [cluster])).toBeNull()
})
import { CNPG_WORKSPACE_KEYS, type CNPGWorkspaceIssue, type CNPGWorkspaceResponse } from './workspace'

const PG = 'postgresql.cnpg.io/v1'
const BARMAN = 'barmancloud.cnpg.io/v1'
const VELERO = 'velero.io/v1'
const PLUGIN = 'barman-cloud.cloudnative-pg.io'

function cluster(name: string, ns = 'pg', spec: any = {}, status: any = {}): any {
  return { apiVersion: PG, kind: 'Cluster', metadata: { name, namespace: ns }, spec, status }
}

function pluginCluster(name: string, store: string, serverName?: string, conditions?: any[]): any {
  return cluster(
    name,
    'pg',
    { plugins: [{ name: PLUGIN, isWALArchiver: true, parameters: { barmanObjectName: store, ...(serverName ? { serverName } : {}) } }] },
    conditions ? { conditions } : {},
  )
}

function backup(name: string, extra: any = {}): any {
  return {
    apiVersion: PG,
    kind: 'Backup',
    metadata: { name, namespace: 'pg', ...(extra.metadata ?? {}) },
    spec: { cluster: { name: 'main' }, ...(extra.spec ?? {}) },
    status: extra.status ?? {},
  }
}

describe('scheduledBackupOf / backupsForScheduledBackup', () => {
  const sched = { apiVersion: PG, kind: 'ScheduledBackup', metadata: { name: 'nightly', namespace: 'pg', uid: 'uid-new' } }
  const ownedBy = (name: string, uid: string, extra: any = {}) =>
    backup(name, {
      ...extra,
      metadata: { ...(extra.metadata ?? {}), ownerReferences: [{ apiVersion: PG, kind: 'ScheduledBackup', name: 'nightly', uid }] },
    })

  it('reads the owner reference name', () => {
    expect(scheduledBackupOf(ownedBy('b1', 'uid-new'))).toBe('nightly')
  })

  it('falls back to the operator label when the owner is the Cluster or unset', () => {
    const b = backup('b1', {
      metadata: {
        labels: { 'cnpg.io/scheduled-backup': 'nightly' },
        ownerReferences: [{ apiVersion: PG, kind: 'Cluster', name: 'main' }],
      },
    })
    expect(scheduledBackupOf(b)).toBe('nightly')
    expect(isBackupFromSchedule(b, sched)).toBe(true)
  })

  it('returns null for an on-demand backup', () => {
    expect(scheduledBackupOf(backup('b1'))).toBeNull()
  })

  it('ignores an owner reference from another API group', () => {
    const b = backup('b1', { metadata: { ownerReferences: [{ apiVersion: 'example.com/v1', kind: 'ScheduledBackup', name: 'nightly' }] } })
    expect(scheduledBackupOf(b)).toBeNull()
  })

  it('does not attribute a recreated schedule the old schedule\'s Backups', () => {
    const old = ownedBy('old', 'uid-old', { metadata: { labels: { 'cnpg.io/scheduled-backup': 'nightly' } } })
    expect(isBackupFromSchedule(old, sched)).toBe(false)
    expect(isBackupFromSchedule(ownedBy('cur', 'uid-new'), sched)).toBe(true)
  })

  it('lists owned backups newest first and never a Velero Backup', () => {
    const owned = (name: string, startedAt: string, apiVersion = PG) => ({
      ...backup(name, { metadata: { labels: { 'cnpg.io/scheduled-backup': 'nightly' } }, status: { startedAt } }),
      apiVersion,
    })
    const list = [
      owned('old', '2026-09-01T00:00:00Z'),
      owned('new', '2026-09-02T00:00:00Z'),
      owned('velero', '2026-09-03T00:00:00Z', VELERO),
      ownedBy('stale', 'uid-old', { status: { startedAt: '2026-09-04T00:00:00Z' } }),
      backup('other'),
      { ...owned('elsewhere', '2026-09-03T00:00:00Z'), metadata: { name: 'elsewhere', namespace: 'x', labels: { 'cnpg.io/scheduled-backup': 'nightly' } } },
    ]
    expect(backupsForScheduledBackup(sched, list).map((b) => b.metadata.name)).toEqual(['new', 'old'])
  })
})

describe('objectStoreForBackup / backupDestination', () => {
  const clusters = [pluginCluster('main', 'store-a')]

  it('ignores the Backup parameter and infers the store from the current Cluster', () => {
    const b = backup('b', { spec: { method: 'plugin', pluginConfiguration: { name: PLUGIN, parameters: { barmanObjectName: 'store-b' } } } })
    expect(objectStoreForBackup(b, clusters)).toEqual({ name: 'store-a', inferred: true })
    expect(backupDestination(b, clusters)).toEqual({ type: 'objectStore', name: 'store-a', inferred: true })
  })

  it("marks a store taken from the Cluster's current plugin as inferred", () => {
    const b = backup('b', { spec: { method: 'plugin', pluginConfiguration: { name: PLUGIN } } })
    expect(objectStoreForBackup(b, clusters)).toEqual({ name: 'store-a', inferred: true })
    expect(backupDestination(b, clusters)).toEqual({ type: 'objectStore', name: 'store-a', inferred: true })
  })

  it('cannot resolve a Backup parameter without a configured, readable target Cluster', () => {
    const b = backup('b', { spec: { method: 'plugin', pluginConfiguration: { name: PLUGIN, parameters: { barmanObjectName: 'store-b' } } } })
    for (const targets of [[], [cluster('main')], [pluginCluster('other', 'store-a')], [{ ...pluginCluster('main', 'store-a'), metadata: { name: 'main', namespace: 'other' } }], [cluster('main', 'pg', { plugins: [{ name: PLUGIN, enabled: false, parameters: { barmanObjectName: 'store-a' } }] })]]) {
      expect(objectStoreForBackup(b, targets)).toBeNull()
      expect(backupDestination(b, targets)).toEqual({ type: 'unknown' })
    }
  })

  it('checks plugin identity before reading barmanObjectName', () => {
    const other = backup('b', { spec: { method: 'plugin', pluginConfiguration: { name: 'other.example.com', parameters: { barmanObjectName: 'store-b' } } } })
    expect(objectStoreForBackup(other, clusters)).toBeNull()
    const unnamed = backup('b', { spec: { method: 'plugin', pluginConfiguration: { parameters: { barmanObjectName: 'store-b' } } } })
    expect(objectStoreForBackup(unnamed, clusters)).toBeNull()
  })

  it('reports in-tree paths and volume snapshots', () => {
    expect(backupDestination(backup('b', { status: { method: 'barmanObjectStore', destinationPath: 's3://x' } }), clusters)).toEqual({ type: 'path', path: 's3://x' })
    expect(backupDestination(backup('b', { spec: { method: 'volumeSnapshot' } }), clusters)).toEqual({ type: 'volumeSnapshot' })
    expect(backupDestination(backup('b'), clusters)).toEqual({ type: 'unknown' })
  })
})

describe('barman-cloud schedule destinations', () => {
  const schedule = { apiVersion: PG, kind: 'ScheduledBackup', metadata: { name: 'nightly', namespace: 'pg' }, spec: { cluster: { name: 'main' }, method: 'plugin', pluginConfiguration: { name: PLUGIN, parameters: { barmanObjectName: 'schedule-store', serverName: 'schedule-server' } } } }

  it('blocks a schedule override when the Cluster plugin has no destination', () => {
    const target = cluster('main', 'pg', { plugins: [{ name: PLUGIN }] })
    expect(cnpgScheduleDestinationBlocker(schedule, [target])).toBe('No backup destination')
    expect(objectStoreForBackup(schedule, [target])).toBeNull()
  })

  it('uses the configured Cluster destination despite a conflicting schedule override', () => {
    const target = pluginCluster('main', 'cluster-store')
    expect(cnpgScheduleDestinationBlocker(schedule, [target])).toBeNull()
    expect(objectStoreForBackup(schedule, [target])).toEqual({ name: 'cluster-store', inferred: true })
    expect(cnpgScheduleDestinationBlocker(schedule, [cluster('main', 'pg', { plugins: [{ name: PLUGIN, enabled: false, parameters: { barmanObjectName: 'cluster-store' } }] })])).toBe('No backup destination')
  })
})

describe('ObjectStore users and inferred health', () => {
  const store = {
    apiVersion: BARMAN,
    kind: 'ObjectStore',
    metadata: { name: 'store', namespace: 'pg' },
    status: {
      serverRecoveryWindow: {
        'srv-a': { firstRecoverabilityPoint: '2026-09-01T00:00:00Z', lastSuccessfulBackupTime: '2026-09-02T00:00:00Z' },
        b: { lastSuccessfulBackupTime: '2026-09-02T00:00:00Z', lastFailedBackupTime: '2026-09-03T00:00:00Z' },
      },
    },
  }
  const ok = [{ type: 'ContinuousArchiving', status: 'True' }]

  it('finds users in the same namespace, keyed by serverName', () => {
    const clusters = [
      pluginCluster('a', 'store', 'srv-a', ok),
      pluginCluster('b', 'store'),
      pluginCluster('c', 'other'),
      { ...pluginCluster('d', 'store'), metadata: { name: 'd', namespace: 'elsewhere' } },
      { ...pluginCluster('e', 'store'), apiVersion: 'cluster.x-k8s.io/v1beta1' },
    ]
    const users = usersOfObjectStore(store, clusters)
    expect(users.map((u) => [u.cluster.metadata.name, u.serverName])).toEqual([
      ['a', 'srv-a'],
      ['b', 'b'],
    ])
  })

  it('reports failing when any user has a failure newer than its last success', () => {
    const users = usersOfObjectStore(store, [pluginCluster('a', 'store', 'srv-a', ok), pluginCluster('b', 'store', undefined, ok)])
    const h = inferredObjectStoreHealth(store, users)
    expect(h.summary.tone).toBe('unhealthy')
    expect(h.summary.text).toBe('Uploads failing for 1 of 2 clusters')
    expect(h.summary.at).toBe('2026-09-03T00:00:00Z')
  })

  it('reports failing from a False ContinuousArchiving condition', () => {
    const users = usersOfObjectStore(store, [pluginCluster('a', 'store', 'srv-a', [{ type: 'ContinuousArchiving', status: 'False', message: 'boom' }])])
    expect(inferredObjectStoreHealth(store, users).summary).toMatchObject({ text: 'Uploads failing', tone: 'unhealthy' })
  })

  it('is healthy only when every user reports archiving', () => {
    const users = usersOfObjectStore(store, [pluginCluster('a', 'store', 'srv-a', ok)])
    expect(inferredObjectStoreHealth(store, users).summary.tone).toBe('healthy')
    const unreported = usersOfObjectStore(store, [pluginCluster('a', 'store', 'srv-a')])
    expect(inferredObjectStoreHealth(store, unreported).summary).toMatchObject({ text: 'No failures reported', tone: 'unknown' })
  })

  it('says so when nothing uses the store', () => {
    expect(inferredObjectStoreHealth(store, []).summary).toMatchObject({ text: 'No visible cluster uses this store', tone: 'unknown' })
  })
})

describe('clustersUsingCatalog', () => {
  const catalog = { apiVersion: PG, kind: 'ImageCatalog', metadata: { name: 'pg', namespace: 'pg' } }
  const clusterCatalog = { apiVersion: PG, kind: 'ClusterImageCatalog', metadata: { name: 'pg' } }

  it('treats an omitted ref kind as ImageCatalog and stays in the namespace', () => {
    const clusters = [
      cluster('a', 'pg', { imageCatalogRef: { name: 'pg', major: 16 } }),
      cluster('b', 'pg', { imageCatalogRef: { kind: 'ImageCatalog', name: 'pg', major: 17 } }),
      cluster('c', 'other', { imageCatalogRef: { name: 'pg', major: 16 } }),
      cluster('d', 'pg', { imageCatalogRef: { kind: 'ClusterImageCatalog', name: 'pg', major: 16 } }),
    ]
    expect(clustersUsingCatalog(catalog, clusters).map((u) => [u.cluster.metadata.name, u.major])).toEqual([
      ['a', 16],
      ['b', 17],
    ])
  })

  it('matches ClusterImageCatalog refs from any namespace, never an omitted kind', () => {
    const clusters = [
      cluster('a', 'x', { imageCatalogRef: { kind: 'ClusterImageCatalog', name: 'pg', major: 16 } }),
      cluster('b', 'y', { imageCatalogRef: { kind: 'ClusterImageCatalog', name: 'pg' } }),
      cluster('c', 'pg', { imageCatalogRef: { name: 'pg', major: 16 } }),
    ]
    expect(clustersUsingCatalog(clusterCatalog, clusters).map((u) => [u.cluster.metadata.name, u.major])).toEqual([
      ['a', 16],
      ['b', null],
    ])
  })
})

describe('issuesForObject', () => {
  const issue = (over: Partial<CNPGWorkspaceIssue>): CNPGWorkspaceIssue => ({
    id: Math.random().toString(),
    severity: 'warning',
    kind: 'Backup',
    group: 'postgresql.cnpg.io',
    namespace: 'pg',
    name: 'b1',
    reason: 'CNPGBackupFailed',
    ...over,
  })

  it('matches kind, group, namespace and name, critical first', () => {
    const issues = [
      issue({ id: 'w' }),
      issue({ id: 'c', severity: 'critical' }),
      issue({ id: 'velero', group: 'velero.io' }),
      issue({ id: 'ns', namespace: 'other' }),
      issue({ id: 'name', name: 'b2' }),
      issue({ id: 'kind', kind: 'ScheduledBackup' }),
    ]
    const got = issuesForObject(issues, { kind: 'Backup', group: 'postgresql.cnpg.io', namespace: 'pg', name: 'b1' })
    expect(got.map((i) => i.id)).toEqual(['c', 'w'])
  })
})

describe('declarations', () => {
  it('distinguishes pending from failed', () => {
    expect(appliedFact({ status: {} }).tone).toBe('unknown')
    expect(appliedFact({ status: { applied: false } }).text).toBe('Not applied')
    expect(appliedFact({ status: { applied: true } }).text).toBe('Applied')
  })

  it('names a missing role only when it is absent from managed roles', () => {
    const db = { status: { applied: false, message: 'role "app" does not exist' } }
    expect(missingManagedRole(db, cluster('main', 'pg', { managed: { roles: [{ name: 'other' }] } }))).toBe('app')
    expect(missingManagedRole(db, cluster('main', 'pg', { managed: { roles: [{ name: 'app' }] } }))).toBeNull()
    expect(missingManagedRole(db, null)).toBeNull()
    expect(missingManagedRole({ status: { message: 'connection refused' } }, cluster('main'))).toBeNull()
  })

  const decl = (kind: string, name: string, clusterName: string, dbname: string, ns = 'pg') => ({
    apiVersion: PG,
    kind,
    metadata: { name, namespace: ns },
    spec: { cluster: { name: clusterName }, dbname },
  })
  const database = { apiVersion: PG, kind: 'Database', metadata: { name: 'app-db', namespace: 'pg' }, spec: { cluster: { name: 'main' }, name: 'app' } }

  it('finds publications and subscriptions on the same cluster and database', () => {
    const pubs = [decl('Publication', 'p1', 'main', 'app'), decl('Publication', 'p2', 'main', 'other'), decl('Publication', 'p3', 'other', 'app')]
    const subs = [decl('Subscription', 's1', 'main', 'app'), decl('Subscription', 's2', 'main', 'app', 'x')]
    const r = replicationForDatabase(database, pubs, subs)
    expect(r.publications.map((p) => p.metadata.name)).toEqual(['p1'])
    expect(r.subscriptions.map((s) => s.metadata.name)).toEqual(['s1'])
  })

  it('finds the Database declaring a publication database', () => {
    expect(databaseForDeclaration(decl('Publication', 'p1', 'main', 'app'), [database])?.metadata.name).toBe('app-db')
    expect(databaseForDeclaration(decl('Publication', 'p1', 'other', 'app'), [database])).toBeNull()
  })
})

describe('relationUnavailable', () => {
  const ws = (state: any, deniedNamespaces?: string[]): CNPGWorkspaceResponse => {
    const coverage: CNPGWorkspaceResponse['coverage'] = {}
    for (const k of CNPG_WORKSPACE_KEYS) coverage[k] = { state: 'full' }
    coverage.backups = { state, deniedNamespaces }
    return { installed: true, context: 'c', namespaces: null, coverage, objects: {}, issues: [], audit: [], backupsOmitted: 0 }
  }

  it('answers from coverage', () => {
    expect(relationUnavailable(ws('full'), 'backups', 'pg', 'Backups')).toBeNull()
    expect(relationUnavailable(ws('partial', ['pg']), 'backups', 'pg', 'Backups')).toBe('No access to Backups')
    expect(relationUnavailable(ws('partial', ['other']), 'backups', 'pg', 'Backups')).toBeNull()
    expect(relationUnavailable(ws('denied'), 'backups', 'pg', 'Backups')).toBe('No access to Backups')
    expect(relationUnavailable(null, 'backups', 'pg', 'Backups')).toBe('Backups could not be read')
  })
})

it('does not apply declaration results from an earlier spec', () => {
  for (const applied of [true, false]) {
    expect(appliedFact({ metadata: { generation: 3 }, status: { applied, observedGeneration: 2 } })).toEqual({ text: 'Pending · awaiting the operator for the current spec', tone: 'unknown' })
  }
  expect(appliedFact({ metadata: { generation: 3 }, status: { applied: true, observedGeneration: 3 } }).tone).toBe('healthy')
})


it('never infers a plugin archive from a replacement Cluster', () => {
 const target = { ...pluginCluster('main', 'new-store'), metadata: { name: 'main', namespace: 'pg', uid: 'new', creationTimestamp: '2026-10-01T00:00:00Z' } }
 for (const status of [{ pluginMetadata: { clusterUID: 'old' } }, { startedAt: '2026-09-30T00:00:00Z' }]) {
  const old = backup('old', { spec: { method: 'plugin', pluginConfiguration: { name: PLUGIN } }, status })
  expect(objectStoreForBackup(old, [target])).toBeNull()
 }
})
