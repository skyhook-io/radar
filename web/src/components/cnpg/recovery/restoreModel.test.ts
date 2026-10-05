import { describe, expect, it } from 'vitest'
import type { CNPGRecoveryResponse } from '../../../api/cnpg-recovery'
import {
  resourcesText,
  restoreBackupDeclared,
  restoreNextSteps,
  restorePermission,
  buildRestoreManifest,
  cnpgRestoredPrimaryUp,
  observeRestore,
  pitrWarnings,
  preflightFacts,
  recoveryEvidenceFor,
  restoreSourceForBackup,
  restoreSourcesFor,
  restoreSourcesForStore,
  targetIsoFrom,
} from './restoreModel'
import { restoreOperationObserver } from './restoreOperation'

const cluster = {
  apiVersion: 'postgresql.cnpg.io/v1',
  metadata: { name: 'pg-a', namespace: 'db' },
  spec: {
    instances: 3,
    imageCatalogRef: { kind: 'ClusterImageCatalog', name: 'std', major: 17 },
    storage: { size: '10Gi', storageClass: 'fast' },
    walStorage: { size: '2Gi' },
    postgresql: { parameters: { max_connections: '200', shared_buffers: '256MB' } },
    plugins: [{ name: 'barman-cloud.cloudnative-pg.io', isWALArchiver: true, parameters: { barmanObjectName: 'store', serverName: 'pg-a-v2' } }],
  },
  status: { conditions: [{ type: 'ContinuousArchiving', status: 'True' }] },
}

const pluginBackup = {
  apiVersion: 'postgresql.cnpg.io/v1',
  metadata: { name: 'b-plugin', namespace: 'db' },
  spec: { cluster: { name: 'pg-a' }, method: 'plugin', pluginConfiguration: { name: 'barman-cloud.cloudnative-pg.io' } },
  status: { phase: 'completed', backupId: '20260929T220000', stoppedAt: '2026-09-29T22:00:09Z' },
}

const store = {
  metadata: { name: 'store', namespace: 'db' },
  status: { serverRecoveryWindow: { 'pg-a-v2': { firstRecoverabilityPoint: '2026-09-20T00:00:00Z', lastSuccessfulBackupTime: '2026-09-29T22:00:09Z' } } },
}

describe('restore sources', () => {
  it('restores a plugin Backup through its ObjectStore with the backup ID pinned', () => {
    expect(restoreSourceForBackup(pluginBackup, cluster)).toEqual({
      kind: 'objectStore',
      objectStore: 'store',
      serverName: 'pg-a-v2',
      backupID: '20260929T220000',
      backupName: 'b-plugin',
      backupEnd: '2026-09-29T22:00:09Z',
    })
    const inTree = { ...pluginBackup, spec: { cluster: { name: 'pg-a' } }, status: { phase: 'completed', stoppedAt: 'x' } }
    expect(restoreSourceForBackup(inTree, cluster)).toMatchObject({ kind: 'backup', backup: 'b-plugin' })
    expect(restoreSourceForBackup({ ...pluginBackup, status: { phase: 'running' } }, cluster)).toBeNull()
  })

  it('lists the store first, then completed Backups of this cluster only', () => {
    const other = { ...pluginBackup, metadata: { name: 'x', namespace: 'db' }, spec: { ...pluginBackup.spec, cluster: { name: 'other' } } }
    const sources = restoreSourcesFor(cluster, [pluginBackup, other])
    expect(sources.map((s) => s.kind)).toEqual(['objectStore', 'objectStore'])
    expect(sources[1]).toMatchObject({ backupName: 'b-plugin' })
  })

  it('offers one source per server an ObjectStore reports', () => {
    expect(restoreSourcesForStore(store)).toEqual([{ kind: 'objectStore', objectStore: 'store', serverName: 'pg-a-v2' }])
  })
})

describe('restore manifest', () => {
  it('copies what the data needs, reads the source store and never archives into it', () => {
    const m = buildRestoreManifest({ sourceCluster: cluster, source: { kind: 'objectStore', objectStore: 'store', serverName: 'pg-a-v2' }, namespace: 'db', newName: 'pg-a-restore', target: { kind: 'time', iso: '2026-09-28T12:00:00Z' } })
    expect(m.spec.bootstrap.recovery).toEqual({ source: 'origin', recoveryTarget: { targetTime: '2026-09-28T12:00:00Z' } })
    expect(m.spec.externalClusters[0].plugin.parameters).toEqual({ barmanObjectName: 'store', serverName: 'pg-a-v2' })
    expect(m.spec.plugins).toBeUndefined()
    expect(m.spec.backup).toBeUndefined()
    expect(m.spec).toMatchObject({ instances: 3, walStorage: { size: '2Gi' }, postgresql: { parameters: { max_connections: '200' } } })
    expect(m.spec.imageCatalogRef.major).toBe(17)
  })

  it('stops at the end of a pinned backup with targetImmediate', () => {
    const source = restoreSourceForBackup(pluginBackup, cluster)!
    const end = buildRestoreManifest({ sourceCluster: cluster, source, namespace: 'db', newName: 'r', target: { kind: 'backupEnd' } })
    expect(end.spec.bootstrap.recovery.recoveryTarget).toEqual({ backupID: '20260929T220000', targetImmediate: true })
    const latest = buildRestoreManifest({ sourceCluster: cluster, source, namespace: 'db', newName: 'r', target: { kind: 'latest' } })
    expect(latest.spec.bootstrap.recovery.recoveryTarget).toEqual({ backupID: '20260929T220000' })
  })

  it('restores an in-tree Backup by reference', () => {
    const m = buildRestoreManifest({ sourceCluster: cluster, source: { kind: 'backup', backup: 'b1' }, namespace: 'db', newName: 'r', target: { kind: 'latest' } })
    expect(m.spec.bootstrap.recovery).toEqual({ backup: { name: 'b1' } })
    expect(m.spec.externalClusters).toBeUndefined()
  })

  it('lists copied facts and says what is missing without a source cluster', () => {
    const facts = preflightFacts(cluster)
    expect(facts.find((f) => f.path === 'spec.walStorage')?.copied).toBe(true)
    expect(facts.find((f) => f.path === 'spec.postgresql.parameters')?.value).toContain('max_connections')
    const none = preflightFacts(null)
    expect(none.every((f) => !f.copied)).toBe(true)
  })
})

describe('PITR evidence', () => {
  const evidence = recoveryEvidenceFor({ kind: 'objectStore', objectStore: 'store', serverName: 'pg-a-v2' }, { sourceCluster: cluster, stores: [store], backups: [pluginBackup], namespace: 'db' })

  it('reads the window from the store and archiving from the condition, naming sources', () => {
    expect(evidence.firstPoint).toEqual({ at: '2026-09-20T00:00:00Z', source: 'ObjectStore store status, server pg-a-v2' })
    expect(evidence.lastBackup?.at).toBe('2026-09-29T22:00:09Z')
    expect(evidence.archiving.tone).toBe('healthy')
    expect(evidence.gaps.some((g) => g.includes('last archived WAL'))).toBe(true)
  })

  it('says the primary was unreachable rather than that it reported nothing', () => {
    const runtime = {
      permission: { proxy: 'allowed' },
      instances: [{ pod: 'pg-a-1', role: 'primary', status: { state: 'unreachable', error: 'the Pod did not answer on port 8000 within 5 s' }, metrics: { state: 'unreachable' } }],
    } as any
    const e = recoveryEvidenceFor({ kind: 'objectStore', objectStore: 'store', serverName: 'pg-a-v2' }, { sourceCluster: cluster, stores: [store], backups: [pluginBackup], namespace: 'db', runtime })
    expect(e.gaps).toContain('The primary’s instance manager could not be read (the Pod did not answer on port 8000 within 5 s), so the last archived WAL time is unknown')
  })

  it('warns, never blocks, outside the evidence', () => {
    const now = Date.parse('2026-09-30T12:00:00Z')
    expect(pitrWarnings('2026-09-25T00:00:00Z', evidence, now)).toEqual([])
    expect(pitrWarnings('2026-09-01T00:00:00Z', evidence, now)[0]).toContain('before the first recoverability point')
    expect(pitrWarnings('2026-09-30T06:00:00Z', evidence, now)[0]).toContain('after the newest evidence')
    expect(pitrWarnings('2026-10-01T00:00:00Z', evidence, now)[0]).toContain('future')
  })

  it('reads the target in the chosen zone and writes UTC without milliseconds', () => {
    expect(targetIsoFrom('2026-09-28T12:00', 'utc')).toBe('2026-09-28T12:00:00Z')
    expect(targetIsoFrom('2026-09-28T12:00:30', 'utc')).toBe('2026-09-28T12:00:30Z')
    expect(targetIsoFrom('nope', 'utc')).toBeNull()
  })
})

function snapshot(over: Partial<CNPGRecoveryResponse> & { phase?: string; ready?: number | null; instances?: number | null }): CNPGRecoveryResponse {
  return {
    cluster: { uid: 'u', phase: over.phase, instances: over.instances ?? 1, readyInstances: over.ready === undefined ? 0 : over.ready },
    recovery: { sourceKind: 'objectStore', objectStore: 'store', serverName: 'pg-a' },
    pods: over.pods ?? [],
    jobs: over.jobs ?? [],
    events: [],
    coverage: over.coverage ?? { pods: { state: 'ok' }, jobs: { state: 'ok' }, events: { state: 'ok' } },
    capturedAt: '2026-09-30T00:00:00Z',
  }
}

describe('observeRestore', () => {
  const recoveryPod = (init: 'running' | 'crash') => ({
    name: 'r-1-full-recovery-x',
    uid: 'p',
    kind: 'job' as const,
    job: 'r-1-full-recovery',
    ownerVerified: true,
    phase: init === 'crash' ? 'Pending' : 'Running',
    ready: false,
    initContainers: [init === 'crash' ? { name: 'bootstrap-controller', state: 'waiting' as const, reason: 'CrashLoopBackOff', restarts: 3, ready: false } : { name: 'bootstrap-controller', state: 'terminated' as const, exitCode: 0, restarts: 0, ready: false }],
    containers: [{ name: 'full-recovery', state: init === 'crash' ? ('waiting' as const) : ('running' as const), restarts: 0, ready: false }],
  })

  it('follows the recovery Pod and points at its logs', () => {
    const o = observeRestore(snapshot({ phase: 'Setting up primary', pods: [recoveryPod('running')] }))
    expect(o.state).toBe('progressing')
    expect(o.logsPod).toEqual({ name: 'r-1-full-recovery-x', container: 'full-recovery' })
  })

  it('surfaces a crashing init container', () => {
    const o = observeRestore(snapshot({ phase: 'Setting up primary', pods: [recoveryPod('crash')] }))
    expect(o.tone).toBe('alert')
    expect(o.logsPod?.container).toBe('bootstrap-controller')
  })

  it('fails on a failed Job and completes only when healthy with every instance ready', () => {
    expect(observeRestore(snapshot({ jobs: [{ name: 'j', active: 0, succeeded: 0, failed: 6, complete: false, failedWith: 'BackoffLimitExceeded: Job has reached the specified backoff limit' }] })).state).toBe('failed')
    expect(observeRestore(snapshot({ phase: 'Cluster in healthy state', ready: 1 })).state).toBe('completed')
    expect(observeRestore(snapshot({ phase: 'Cluster in healthy state', ready: 1, instances: 3 })).state).toBe('progressing')
  })

  it('never reads missing access as progress or failure', () => {
    const o = observeRestore(snapshot({ coverage: { pods: { state: 'denied', grant: { verb: 'list', resource: 'pods', namespace: 'db' } }, jobs: { state: 'denied' }, events: { state: 'denied' } } }))
    expect(o.state).toBe('unobservable')
    expect(o.detail).toContain('list pods')
  })
})

describe('restore operation observer', () => {
  const op = { id: '1', kind: 'restore', label: 'Restore into r', context: 'c', namespace: 'db', cluster: 'r', startedAt: 0, baseline: {}, state: 'requested' as const, lastProgressAt: 0 }

  it('waits for the Cluster, then completes on a healthy phase with all instances ready', () => {
    expect(restoreOperationObserver(op, { now: 1000 }).state).toBe('requested')
    const progressing = restoreOperationObserver(op, { now: 1000, cluster: { spec: { instances: 1 }, status: { phase: 'Setting up primary', readyInstances: 0 } } })
    expect(progressing.state).not.toBe('completed')
    const done = restoreOperationObserver(op, {
      now: 1000,
      cluster: { spec: { instances: 1 }, status: { phase: 'Cluster in healthy state', readyInstances: 1 } },
      ha: { jobs: { state: 'ok', items: [{ name: 'r-1-full-recovery', role: 'full-recovery', phase: 'succeeded' }] } } as any,
    })
    expect(done.state).toBe('completed')
  })

  it('completes without Job access once the restored cluster is healthy and ready', () => {
    const done = restoreOperationObserver(op, {
      now: 1000,
      cluster: { spec: { instances: 1 }, status: { phase: 'Cluster in healthy state', readyInstances: 1 } },
      ha: { jobs: { state: 'denied' } } as any,
    })
    expect(done.state).toBe('completed')
  })

  it('fails when the recovery Job fails', () => {
    const r = restoreOperationObserver(op, {
      now: 1000,
      cluster: { spec: { instances: 1 }, status: { phase: 'Setting up primary' } },
      ha: { jobs: { state: 'ok', items: [{ name: 'r-1-full-recovery', role: 'full-recovery', phase: 'failed', reason: 'BackoffLimitExceeded' }] } } as any,
    })
    expect(r.state).toBe('failed')
  })
})

describe('restorePermission', () => {
  it('blocks review with the grant when create clusters is denied, whichever way the dialog opened', () => {
    const r = restorePermission('db', { allowed: false, permission: 'denied', grant: { verb: 'create', group: 'postgresql.cnpg.io', resource: 'clusters', namespace: 'db' }, reason: 'x' }, undefined)
    expect(r.blocked).toContain('needs create clusters (postgresql.cnpg.io) in namespace db')
  })
  it('blocks with the refusal when the operator webhook rejects writes', () => {
    expect(restorePermission('db', { allowed: false, permission: 'allowed', reason: 'The API server would reject it: no ready endpoint' }, undefined).blocked).toContain('no ready endpoint')
  })
  it('is pending while checking, open when allowed, and unchecked (not blocked) when the check failed', () => {
    expect(restorePermission('db', undefined, undefined).pending).toBeDefined()
    expect(restorePermission('db', { allowed: true, permission: 'allowed' }, undefined)).toEqual({})
    const failed = restorePermission('db', undefined, new Error('boom'))
    expect(failed.blocked).toBeUndefined()
    expect(failed.unchecked).toContain('boom')
  })
})

describe('restoreNextSteps', () => {
  it('marks done or to do only what Radar can read, never whether applications moved', () => {
    const fresh = restoreNextSteps({ validationRecorded: false, backup: 'none' })
    expect(fresh.map((s) => [s.id, s.state])).toEqual([
      ['connect', 'unknown'],
      ['validate', 'todo'],
      ['backup', 'todo'],
    ])
    expect(fresh[2].note).toContain('no backup destination')
    expect(restoreNextSteps({ validationRecorded: true, backup: 'walArchiving' }).map((s) => s.state)).toEqual(['unknown', 'done', 'done'])
    expect(restoreNextSteps({ validationRecorded: false, backup: undefined })[2].state).toBe('unknown')
  })
  it('never counts volume snapshots alone, or backups without WAL archiving, as done', () => {
    const plugin = (isWALArchiver: boolean) => ({ spec: { plugins: [{ name: 'barman-cloud.cloudnative-pg.io', isWALArchiver, parameters: { barmanObjectName: 'store' } }] } })
    const step = (cluster: any) => restoreNextSteps({ validationRecorded: false, backup: restoreBackupDeclared(cluster) })[2]
    expect(step({ spec: { backup: { volumeSnapshot: { className: 'csi' } } } })).toMatchObject({
      state: 'partial',
      note: 'Volume snapshots declared, but no WAL archiving, so no point-in-time recovery',
    })
    expect(step(plugin(false))).toMatchObject({ state: 'partial' })
    expect(step(plugin(true)).state).toBe('done')
    const noDestination = step({ spec: { plugins: [{ name: 'barman-cloud.cloudnative-pg.io', isWALArchiver: true }] } })
    expect(noDestination.state).toBe('partial')
    expect(noDestination.note).toContain('names no ObjectStore')
    expect(step({ spec: { backup: { barmanObjectStore: { destinationPath: 's3://b' } } } }).state).toBe('done')
    expect(step({ spec: {} }).state).toBe('todo')
  })
})

describe('resourcesText', () => {
  it('reads requests and limits instead of JSON', () => {
    expect(resourcesText({ requests: { cpu: '50m', memory: '128Mi' } })).toBe('requests: cpu 50m, memory 128Mi')
    expect(resourcesText({ requests: { cpu: '50m' }, limits: { memory: '256Mi' } })).toBe('requests: cpu 50m · limits: memory 256Mi')
  })
})

describe('cnpgRestoredPrimaryUp', () => {
  const pod = (kind: 'job' | 'instance', phase: string, ready: boolean) => ({ name: `${kind}-x`, uid: kind, kind, ownerVerified: true, phase, ready, initContainers: [], containers: [] })
  it('is up once a restored instance is ready, even if the cluster later degrades', () => {
    // A replica lost after the restore: not "completed", but the primary answers.
    const degraded = snapshot({ phase: 'Cluster in healthy state', ready: 1, instances: 2, pods: [pod('job', 'Succeeded', false), pod('instance', 'Running', true), pod('instance', 'Running', false)] as any })
    expect(observeRestore(degraded).state).not.toBe('completed')
    expect(cnpgRestoredPrimaryUp(degraded)).toBe(true)
  })
  it('is not up while recovery runs or when Pods cannot be seen, and a join Job does not hide it', () => {
    expect(cnpgRestoredPrimaryUp(snapshot({ pods: [pod('job', 'Running', false)] as any }))).toBe(false)
    // A replica joining after the restore runs a Job too.
    expect(cnpgRestoredPrimaryUp(snapshot({ pods: [pod('job', 'Running', false), pod('instance', 'Running', true)] as any }))).toBe(true)
    expect(cnpgRestoredPrimaryUp(snapshot({ pods: [pod('instance', 'Running', true)] as any, coverage: { pods: { state: 'denied' }, jobs: { state: 'ok' }, events: { state: 'ok' } } as any }))).toBe(false)
  })
})

describe('restore availability and image review', () => {
  it('disables only a readable, empty source list', async () => {
    const { assessRestoreSources } = await import('./restoreModel')
    const sourceCluster = { metadata: { name: 'pg', namespace: 'db' }, spec: {} }
    const data = { objects: { backups: [] }, coverage: { backups: { state: 'full' } } } as any
    expect(assessRestoreSources(data, 'db', sourceCluster).disabledReason).toContain('Nothing to restore')
    for (const state of ['denied', 'error', 'syncing', 'uncached']) {
      const result = assessRestoreSources({ ...data, coverage: { backups: { state } } }, 'db', sourceCluster)
      expect(result.disabledReason).toBeUndefined()
      expect(result.unreadReason).toContain('could not be read')
    }
    const partial = { ...data, coverage: { backups: { state: 'partial', deniedNamespaces: ['other'] } } }
    expect(assessRestoreSources(partial, 'db', sourceCluster).disabledReason).toBeDefined()
    expect(assessRestoreSources(partial, 'other', sourceCluster).disabledReason).toBeUndefined()
    expect(assessRestoreSources(data, 'db', cluster).disabledReason).toBeUndefined()
  })
  it('copies status.image only without a declared image or catalog and labels its origin', () => {
    const source = { kind: 'backup', backup: 'b' } as const
    const build = (sourceCluster: any) => buildRestoreManifest({ sourceCluster, source, namespace: 'db', newName: 'r', target: { kind: 'latest' } }).spec
    const fromStatus = { spec: {}, status: { image: 'pg:17.6' } }
    expect(build(fromStatus).imageName).toBe('pg:17.6')
    expect(preflightFacts(fromStatus).find((f) => f.label === 'Image')).toMatchObject({ copied: true, value: 'pg:17.6 (from status.image)' })
    const declared = { spec: { imageName: 'pg:17.7' }, status: { image: 'pg:17.6' } }
    expect(build(declared).imageName).toBe('pg:17.7')
    const catalog = { ...cluster, status: { image: 'pg:17.6' } }
    expect(build(catalog).imageCatalogRef).toEqual(cluster.spec.imageCatalogRef)
    expect(build(catalog).imageName).toBeUndefined()
    expect(preflightFacts(catalog).find((f) => f.label === 'Image')?.path).toBe('spec.imageCatalogRef')
  })
})
