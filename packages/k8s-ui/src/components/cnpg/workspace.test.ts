import { cnpgDimensions } from './ha'
import { describe, it, expect } from 'vitest'
import { buildCNPGFleet, cnpgReadyInstances, cnpgRecoveryMatchesCluster, getCNPGRestoreValidation, type CNPGWorkspaceResponse, type CNPGWorkspaceKey, CNPG_WORKSPACE_KEYS } from './workspace'

const G = 'postgresql.cnpg.io/v1'

it('does not promote an archiving condition into evidence of an archive destination', () => {
  const c = cluster('payments', 'db', { status: { conditions: [{ type: 'ContinuousArchiving', status: 'True', lastTransitionTime: '2026-10-01T12:00:00Z' }] } })
  const fact = buildCNPGFleet(resp({ clusters: [c] })).rows[0].protection.walArchiving
  expect(fact).toMatchObject({ text: 'Not archived: no destination configured', tone: 'neutral', source: 'Cluster spec' })
  expect(fact.detail).toContain("WAL is not archived to recovery storage")
  expect(fact.operatorCondition).toMatchObject({ type: 'ContinuousArchiving', status: 'True', lastTransitionTime: '2026-10-01T12:00:00Z' })
  const snapshot = cluster('snapshot', 'db', { spec: { backup: { volumeSnapshot: {} } }, status: c.status })
  expect(buildCNPGFleet(resp({ clusters: [snapshot] })).rows[0].protection.walArchiving.text).toBe('Not archived: no destination configured')
})

it('preserves declared archiver failures and distinguishes backup-only and opaque archiver plugins', () => {
  const make = (plugins: any[], status = 'True') => buildCNPGFleet(resp({ clusters: [cluster('pg', 'db', { spec: { plugins }, status: { conditions: [{ type: 'ContinuousArchiving', status, message: 'archive report' }] } })] })).rows[0].protection.walArchiving
  const barman = { name: 'barman-cloud.cloudnative-pg.io', isWALArchiver: true }
  expect(make([barman], 'False')).toMatchObject({ text: 'Failing', tone: 'unhealthy', detail: 'archive report' })
  expect(make([barman])).toMatchObject({ text: 'Not archived: no destination configured', tone: 'neutral' })
  expect(make([{ ...barman, isWALArchiver: false, parameters: { barmanObjectName: 'store' } }])).toMatchObject({ text: 'Not archived: no destination configured', tone: 'neutral' })
  expect(make([{ name: 'third-party-archive', isWALArchiver: true }])).toMatchObject({ text: 'CNPG reports archiving', detail: 'Archive plugin declared; its destination is not assessed here' })
  expect(make([{ ...barman, enabled: false, parameters: { barmanObjectName: 'store' } }])).toMatchObject({ text: 'Not archived: no destination configured', tone: 'neutral' })
})

function cluster(name: string, ns: string, extra: any = {}): any {
  return {
    apiVersion: G,
    kind: 'Cluster',
    metadata: { name, namespace: ns, ...(extra.metadata ?? {}) },
    spec: { instances: 3, ...(extra.spec ?? {}) },
    status: {
      phase: 'Cluster in healthy state',
      readyInstances: 3,
      currentPrimary: `${name}-1`,
      ...(extra.status ?? {}),
    },
  }
}

function pod(name: string, ns: string, clusterName: string, role: string, ready = true): any {
  return {
    apiVersion: 'v1',
    kind: 'Pod',
    metadata: { name, namespace: ns, labels: { 'cnpg.io/cluster': clusterName, 'cnpg.io/instanceRole': role } },
    status: { conditions: [{ type: 'Ready', status: ready ? 'True' : 'False' }] },
  }
}

function serverProblem(reason: string, message: string, kind = 'Cluster', name = 'pg-a', severity: 'critical' | 'warning' = 'warning') {
  return { id: `server:${reason}`, reason, message, kind, name, namespace: 'db', group: 'postgresql.cnpg.io', severity }
}

function resp(objects: Partial<Record<CNPGWorkspaceKey, any[]>>, over: Partial<CNPGWorkspaceResponse> = {}): CNPGWorkspaceResponse {
  const coverage: CNPGWorkspaceResponse['coverage'] = {}
  for (const k of CNPG_WORKSPACE_KEYS) coverage[k] = { state: 'full' }
  return {
    installed: true,
    context: 'test',
    namespaces: null,
    coverage: { ...coverage, ...(over.coverage ?? {}) },
    objects,
    issues: over.issues ?? [],
    audit: over.audit ?? [],
    backupsOmitted: 0,
    ...(over.scheduleReadings ? { scheduleReadings: over.scheduleReadings } : {}),
  }
}

describe('buildCNPGFleet', () => {
  it('never reports replication as healthy from pod readiness alone', () => {
    const fleet = buildCNPGFleet(
      resp({
        clusters: [cluster('pg-a', 'db')],
        pods: [pod('pg-a-1', 'db', 'pg-a', 'primary'), pod('pg-a-2', 'db', 'pg-a', 'replica'), pod('pg-a-3', 'db', 'pg-a', 'replica')],
      }),
    )
    const row = fleet.rows[0]
    expect(row.replication.tone).toBe('unknown')
    expect(row.replication.text).toContain('2/2 Pods ready')
    expect(row.pods[0].role).toBe('primary')
  })

  it('attributes child-object issues to their cluster and marks attention', () => {
    const fleet = buildCNPGFleet(
      resp(
        {
          clusters: [cluster('pg-a', 'db'), cluster('pg-b', 'db')],
          databases: [{ apiVersion: G, kind: 'Database', metadata: { name: 'reporting', namespace: 'db' }, spec: { cluster: { name: 'pg-b' } }, status: { applied: false } }],
        },
        {
          issues: [
            { id: 'i1', severity: 'warning', kind: 'Database', group: 'postgresql.cnpg.io', namespace: 'db', name: 'reporting', reason: 'CNPGDeclarativeNotApplied', message: 'role "x" does not exist' },
          ],
        },
      ),
    )
    const b = fleet.rows.find((r) => r.name === 'pg-b')!
    const a = fleet.rows.find((r) => r.name === 'pg-a')!
    expect(b.attention).toBe(true)
    expect(b.categories.has('declarations')).toBe(true)
    expect(b.declarations.failed).toBe(1)
    expect(a.attention).toBe(false)
    expect(fleet.attentionCount).toBe(1)
    expect(fleet.categoryCounts.declarations).toBe(1)
    expect(fleet.rows[0].name).toBe('pg-b')
  })

  it('orders rows by worst problem, then problem count, then namespace/name', () => {
    const issue = (id: string, severity: 'critical' | 'warning', name: string) => ({
      id, severity, kind: 'Cluster', group: 'postgresql.cnpg.io', namespace: 'db', name, reason: 'CNPGClusterUnhealthy', message: id,
    })
    const fleet = buildCNPGFleet(
      resp(
        { clusters: ['pg-a', 'pg-b', 'pg-c', 'pg-d', 'pg-e'].map((n) => cluster(n, 'db')) },
        {
          issues: [issue('w1', 'warning', 'pg-a'), issue('c1', 'critical', 'pg-b'), issue('w2', 'warning', 'pg-c'), issue('w3', 'warning', 'pg-c')],
          audit: [{ checkId: 'cnpgNoDeclarativeBackup', severity: 'warning', kind: 'Cluster', namespace: 'db', name: 'pg-e', message: 'no ScheduledBackup' }],
        },
      ),
    )
    expect(fleet.rows.map((r) => r.name)).toEqual(['pg-b', 'pg-c', 'pg-a', 'pg-e', 'pg-d'])
  })

  it('treats the no-schedule audit finding as posture, not attention, and words it narrowly', () => {
    const fleet = buildCNPGFleet(
      resp({ clusters: [cluster('pg-a', 'db')] }, {
        audit: [{ checkId: 'cnpgNoDeclarativeBackup', severity: 'warning', kind: 'Cluster', namespace: 'db', name: 'pg-a', message: 'no ScheduledBackup' }],
      }),
    )
    const row = fleet.rows[0]
    expect(row.attention).toBe(false)
    expect(row.problems[0].title).toBe('No declarative backup schedule')
    expect(row.protection.schedule.text).toBe('No declarative schedule')
  })

  it('says "no access" rather than "none" when a kind is not readable', () => {
    const fleet = buildCNPGFleet(
      resp({ clusters: [cluster('pg-a', 'db')] }, { coverage: { backups: { state: 'denied' }, scheduledBackups: { state: 'partial', deniedNamespaces: ['db'] } } }),
    )
    const p = fleet.rows[0].protection
    expect(p.lastSuccessfulBackup.text).toBe('No access to Backups')
    expect(p.lastSuccessfulBackup.tone).toBe('unknown')
    expect(p.schedule.text).toBe('No access to ScheduledBackups')
    expect(fleet.incompleteKinds).toEqual(expect.arrayContaining(['backups', 'scheduledBackups']))
  })

  it('prefers the newest successful backup across Backup CRs and ObjectStore status, citing the source', () => {
    const fleet = buildCNPGFleet(
      resp({
        clusters: [cluster('pg-a', 'db', { spec: { plugins: [{ name: 'barman-cloud.cloudnative-pg.io', parameters: { barmanObjectName: 'store' } }] } })],
        backups: [{ apiVersion: G, kind: 'Backup', metadata: { name: 'pg-a-old', namespace: 'db' }, spec: { cluster: { name: 'pg-a' } }, status: { phase: 'completed', stoppedAt: '2026-09-20T02:00:00Z' } }],
        objectStores: [{
          apiVersion: 'barmancloud.cnpg.io/v1', kind: 'ObjectStore', metadata: { name: 'store', namespace: 'db' },
          status: { serverRecoveryWindow: { 'pg-a': { firstRecoverabilityPoint: '2026-09-01T00:00:00Z', lastSuccessfulBackupTime: '2026-09-28T02:00:00Z' } } },
        }],
      }),
    )
    const p = fleet.rows[0].protection
    expect(p.lastSuccessfulBackup.at).toBe('2026-09-28T02:00:00Z')
    expect(p.lastSuccessfulBackup.source).toBe('ObjectStore store status')
    expect(p.recoveryWindow.from).toBe('2026-09-01T00:00:00Z')
    expect(p.destination.method).toBe('plugin')
  })

  it('ignores deprecated Cluster status backup fields for plugin clusters', () => {
    const fleet = buildCNPGFleet(
      resp({
        clusters: [cluster('pg-a', 'db', {
          spec: { plugins: [{ name: 'barman-cloud.cloudnative-pg.io', parameters: { barmanObjectName: 'store' } }] },
          status: { lastSuccessfulBackup: '2020-01-01T00:00:00Z' },
        })],
      }),
    )
    expect(fleet.rows[0].protection.lastSuccessfulBackup.text).toBe('No successful backup yet')
  })

  it('never marks restore validation healthy', () => {
    const src = cluster('pg-a', 'db', { spec: { plugins: [{ name: 'barman-cloud.cloudnative-pg.io', parameters: { barmanObjectName: 'store' } }] } })
    const restored = cluster('pg-a-restore', 'db', {
      spec: {
        bootstrap: { recovery: { source: 'origin' } },
        externalClusters: [{ name: 'origin', plugin: { name: 'barman-cloud.cloudnative-pg.io', parameters: { barmanObjectName: 'store', serverName: 'pg-a' } } }],
      },
    })
    const fleet = buildCNPGFleet(resp({ clusters: [src, restored] }))
    const a = fleet.rows.find((r) => r.name === 'pg-a')!
    expect(a.protection.restoreValidation.text).toBe('Archive restored into pg-a-restore')
    expect(a.protection.restoreValidation.tone).toBe('neutral')
    const r = fleet.rows.find((x) => x.name === 'pg-a-restore')!
    expect(r.protection.restoreValidation.text).toBe('None recorded')
    expect(r.protection.restoreValidation.tone).toBe('unknown')
  })

  it('matches a restored Cluster’s external source despite conflicting Backup parameters', () => {
    const src = cluster('pg-a', 'db', { spec: { plugins: [{ name: 'barman-cloud.cloudnative-pg.io', parameters: { barmanObjectName: 'cluster-store', serverName: 'cluster-server' } }] } })
    const conflicting = { apiVersion: 'postgresql.cnpg.io/v1', kind: 'Backup', metadata: { name: 'b', namespace: 'db' }, spec: { cluster: { name: 'pg-a' }, method: 'plugin', pluginConfiguration: { name: 'barman-cloud.cloudnative-pg.io', parameters: { barmanObjectName: 'backup-store', serverName: 'backup-server' } } }, status: { phase: 'completed' } }
    const restored = cluster('pg-a-restore', 'db', { spec: { bootstrap: { recovery: { source: 'origin' } }, externalClusters: [{ name: 'origin', plugin: { name: 'barman-cloud.cloudnative-pg.io', parameters: { barmanObjectName: 'cluster-store', serverName: 'cluster-server' } } }] } })
    const fact = buildCNPGFleet(resp({ clusters: [src, restored], backups: [conflicting] })).rows.find((r) => r.name === 'pg-a')!.protection.restoreValidation
    expect(fact).toMatchObject({ text: 'Archive restored into pg-a-restore', tone: 'neutral' })
    const wrongSource = { ...restored, spec: { ...restored.spec, externalClusters: [{ name: 'origin', plugin: { name: 'barman-cloud.cloudnative-pg.io', parameters: conflicting.spec.pluginConfiguration.parameters } }] } }
    expect(buildCNPGFleet(resp({ clusters: [src, wrongSource], backups: [conflicting] })).rows.find((r) => r.name === 'pg-a')!.protection.restoreValidation).toMatchObject({ text: 'None recorded', tone: 'unknown' })
  })

  it('reads a recorded validation note on the restored cluster, still never healthy', () => {
    const plugin = { plugins: [{ name: 'barman-cloud.cloudnative-pg.io', parameters: { barmanObjectName: 'store' } }] }
    const src = cluster('pg-a', 'db', { metadata: { uid: 'uid-a' }, spec: plugin })
    const restoredSpec = {
      bootstrap: { recovery: { source: 'origin' } },
      externalClusters: [{ name: 'origin', plugin: { name: 'barman-cloud.cloudnative-pg.io', parameters: { barmanObjectName: 'store', serverName: 'pg-a' } } }],
    }
    const note = (uid: string) =>
      JSON.stringify({ version: 1, recordedAt: '2026-09-29T10:00:00Z', recordedBy: 'alice', checked: 'row counts on orders', source: { namespace: 'db', name: 'pg-a', uid, verified: true } })
    const noted = cluster('pg-a-restore', 'db', { metadata: { annotations: { 'radar.skyhook.io/restore-validation': note('uid-a') } }, spec: restoredSpec })
    const fleet = buildCNPGFleet(resp({ clusters: [src, noted] }))
    const fact = fleet.rows.find((r) => r.name === 'pg-a')!.protection.restoreValidation
    expect(fact.text).toBe('Validation recorded')
    expect(fact.tone).toBe('neutral')
    expect(fact.at).toBe('2026-09-29T10:00:00Z')
    expect(fact.source).toContain('by alice on pg-a-restore')

    const otherUID = cluster('pg-a-restore', 'db', { metadata: { annotations: { 'radar.skyhook.io/restore-validation': note('uid-previous-incarnation') } }, spec: restoredSpec })
    expect(buildCNPGFleet(resp({ clusters: [src, otherUID] })).rows.find((r) => r.name === 'pg-a')!.protection.restoreValidation.text).toBe('None recorded')

    const malformed = cluster('pg-a-restore', 'db', { metadata: { annotations: { 'radar.skyhook.io/restore-validation': '{not json' } }, spec: restoredSpec })
    expect(buildCNPGFleet(resp({ clusters: [src, malformed] })).rows.find((r) => r.name === 'pg-a')!.protection.restoreValidation.text).toBe('Archive restored into pg-a-restore')
  })

  it('ends the recovery window at WAL archiving, not at the last base backup', () => {
    const plugin = { plugins: [{ name: 'barman-cloud.cloudnative-pg.io', isWALArchiver: true, parameters: { barmanObjectName: 'store' } }] }
    const store = {
      apiVersion: 'barmancloud.cnpg.io/v1', kind: 'ObjectStore', metadata: { name: 'store', namespace: 'db' },
      status: { serverRecoveryWindow: { 'pg-a': { firstRecoverabilityPoint: '2026-09-01T00:00:00Z', lastSuccessfulBackupTime: '2026-09-02T00:00:00Z', lastFailedBackupTime: '2026-09-03T00:00:00Z' } } },
    }
    const archiving = cluster('pg-a', 'db', { spec: plugin, status: { conditions: [{ type: 'ContinuousArchiving', status: 'True' }] } })
    const w = buildCNPGFleet(resp({ clusters: [archiving], objectStores: [store] })).rows[0].protection.recoveryWindow
    expect(w).toMatchObject({ from: '2026-09-01T00:00:00Z', tone: 'neutral' })
    expect(w).not.toHaveProperty('to')
    const failing = cluster('pg-a', 'db', { spec: plugin, status: { conditions: [{ type: 'ContinuousArchiving', status: 'False' }] } })
    expect(buildCNPGFleet(resp({ clusters: [failing], objectStores: [store] })).rows[0].protection.recoveryWindow.tone).toBe('degraded')
  })

  it('qualifies archiving as the operator report with its transition time', () => {
    const hourAgo = new Date(Date.now() - 3_600_000).toISOString()
    const resumed = cluster('pg-a', 'db', { spec: { backup: { barmanObjectStore: { destinationPath: 's3://backups' } } }, metadata: { creationTimestamp: '2026-01-01T00:00:00Z' }, status: { conditions: [{ type: 'ContinuousArchiving', status: 'True', lastTransitionTime: hourAgo }] } })
    expect(buildCNPGFleet(resp({ clusters: [resumed] })).rows[0].protection.walArchiving).toMatchObject({ text: 'CNPG reports archiving', source: 'Cluster status · ContinuousArchiving=True', at: hourAgo, atMeaning: 'since' })
    const old = cluster('pg-a', 'db', { spec: { backup: { barmanObjectStore: { destinationPath: 's3://backups' } } }, metadata: { creationTimestamp: '2026-01-01T00:00:00Z' }, status: { conditions: [{ type: 'ContinuousArchiving', status: 'True', lastTransitionTime: '2026-01-01T00:02:00Z' }] } })
    expect(buildCNPGFleet(resp({ clusters: [old] })).rows[0].protection.walArchiving.at).toBe('2026-01-01T00:02:00Z')
  })

  it('reports WAL archiving from the condition and unknown when absent', () => {
    const failing = cluster('pg-a', 'db', { spec: { backup: { barmanObjectStore: { destinationPath: 's3://backups' } } }, status: { conditions: [{ type: 'ContinuousArchiving', status: 'False', message: 'exit status 1' }] } })
    const silent = cluster('pg-b', 'db', { spec: { backup: { barmanObjectStore: { destinationPath: 's3://backups' } } } })
    const fleet = buildCNPGFleet(resp({ clusters: [failing, silent] }))
    expect(fleet.rows.find((r) => r.name === 'pg-a')!.protection.walArchiving.tone).toBe('unhealthy')
    expect(fleet.rows.find((r) => r.name === 'pg-a')!.protection.summary.text).toBe('WAL archiving failing')
    expect(fleet.rows.find((r) => r.name === 'pg-b')!.protection.walArchiving.tone).toBe('unknown')
  })

  it('ignores same-named kinds from other API groups', () => {
    const capi = { apiVersion: 'cluster.x-k8s.io/v1beta1', kind: 'Cluster', metadata: { name: 'workload', namespace: 'db' } }
    const fleet = buildCNPGFleet(resp({ clusters: [capi, cluster('pg-a', 'db')] }))
    expect(fleet.rows.map((r) => r.name)).toEqual(['pg-a'])
  })

  it('counts declared managed roles and their reconcile errors', () => {
    const c = cluster('pg-a', 'db', {
      spec: { managed: { roles: [{ name: 'app' }, { name: 'audit' }] } },
      status: { managedRolesStatus: { cannotReconcile: { audit: ['permission denied'] } } },
    })
    const d = buildCNPGFleet(resp({ clusters: [c] })).rows[0].declarations
    expect(d.total).toBe(2)
    expect(d.failed).toBe(1)
    expect(d.summary.tone).toBe('degraded')
  })

  it('treats declared managed roles without status as pending, not reconciled', () => {
    const c = cluster('pg-a', 'db', { spec: { managed: { roles: [{ name: 'app' }, { name: 'audit' }] } } })
    const d = buildCNPGFleet(resp({ clusters: [c] })).rows[0].declarations
    expect(d.pending).toBe(2)
    expect(d.summary.text).toBe('2 of 2 pending')
  })

  it('does not claim "no schedule" when ScheduledBackups are unreadable', () => {
    const fleet = buildCNPGFleet(resp({ clusters: [cluster('pg-a', 'db')] }, { coverage: { scheduledBackups: { state: 'denied' } } }))
    expect(fleet.rows[0].protection.summary.text).not.toBe('No backup destination or schedule')
  })

  it('says recovery is only declared until the restored cluster has a ready instance', () => {
    const src = cluster('pg-a', 'db')
    const restoring = cluster('pg-a-restore', 'db', {
      spec: { bootstrap: { recovery: { backup: { name: 'nightly-1' } } } },
      status: { readyInstances: 0, currentPrimary: undefined },
    })
    const backups = [{ apiVersion: G, kind: 'Backup', metadata: { name: 'nightly-1', namespace: 'db' }, spec: { cluster: { name: 'pg-a' } }, status: { phase: 'completed' } }]
    const a = buildCNPGFleet(resp({ clusters: [src, restoring], backups })).rows.find((r) => r.name === 'pg-a')!
    expect(a.protection.restoreValidation.text).toBe('Recovery declared in pg-a-restore')
    expect(a.protection.restoreValidation.tone).toBe('unknown')
  })

  it('does not say no restore was recorded when the Backup it names is unreadable', () => {
    const src = cluster('pg-a', 'db')
    const restoring = cluster('pg-a-restore', 'db', { spec: { bootstrap: { recovery: { backup: { name: 'nightly-1' } } } } })
    const a = buildCNPGFleet(resp({ clusters: [src, restoring] }, { coverage: { backups: { state: 'denied' } } })).rows.find((r) => r.name === 'pg-a')!
    expect(a.protection.restoreValidation.text).toBe('Unknown: no access to Backups')
  })

  it('does not attribute a restore by backup-name prefix alone', () => {
    const src = cluster('pg', 'db')
    const other = cluster('pg-orders-restore', 'db', { spec: { bootstrap: { recovery: { backup: { name: 'pg-orders-backup' } } } } })
    const backups = [{ apiVersion: G, kind: 'Backup', metadata: { name: 'pg-orders-backup', namespace: 'db' }, spec: { cluster: { name: 'pg-orders' } }, status: { phase: 'completed' } }]
    const row = buildCNPGFleet(resp({ clusters: [src, other], backups })).rows.find((r) => r.name === 'pg')!
    expect(row.protection.restoreValidation.text).toBe('None recorded')
  })

  it('marks Poolers unknown when they are not readable', () => {
    const fleet = buildCNPGFleet(resp({ clusters: [cluster('pg-a', 'db')] }, { coverage: { poolers: { state: 'denied' } } }))
    expect(fleet.rows[0].poolersKnown).toBe(false)
  })

  it('attributes instance Pod issues to their cluster', () => {
    const fleet = buildCNPGFleet(
      resp({ clusters: [cluster('pg-a', 'db')], pods: [pod('pg-a-2', 'db', 'pg-a', 'replica', false)] }, {
        issues: [{ id: 'p1', severity: 'critical', kind: 'Pod', namespace: 'db', name: 'pg-a-2', reason: 'CrashLoopBackOff', message: 'Back-off restarting failed container' }],
      }),
    )
    expect(fleet.rows[0].attention).toBe(true)
    expect(fleet.rows[0].categories.has('availability')).toBe(true)
  })

  it("names a Cluster's own Job Pod problem by what the Job is for, as the cause, never as an instance", () => {
    const joinPod = {
      apiVersion: 'v1',
      kind: 'Pod',
      metadata: { name: 'pg-a-2-join-x1', namespace: 'db', labels: { 'cnpg.io/cluster': 'pg-a', 'cnpg.io/jobRole': 'join', 'cnpg.io/instanceName': 'pg-a-2' } },
      status: { phase: 'Pending' },
    }
    const base = resp({ clusters: [cluster('pg-a', 'db', { status: { readyInstances: 1 } })], pods: [pod('pg-a-1', 'db', 'pg-a', 'primary')] }, {
      issues: [
        { id: 'c1', severity: 'warning', category: 'operator_condition_failed', kind: 'Cluster', group: 'postgresql.cnpg.io', namespace: 'db', name: 'pg-a', reason: 'Ready: ClusterIsNotReady', message: 'Cluster Is Not Ready' },
        { id: 'j1', severity: 'critical', category: 'unschedulable', kind: 'Pod', namespace: 'db', name: 'pg-a-2-join-x1', reason: 'Unschedulable', message: '2 node(s) insufficient pods (0/2 nodes available)' },
      ],
    })
    const row = buildCNPGFleet({ ...base, jobPods: [joinPod] }).rows[0]
    expect(row.problems[0].title).toBe("New standby pg-a-2: Can't be scheduled")
    expect(row.problems[0].severity).toBe('warning')
    expect(row.problems[0].detail).toBe('both nodes have reached their Pod limit')
    expect(row.problems[0].rawDetail).toBe('2 node(s) insufficient pods (0/2 nodes available)')
    expect(row.problems[0].origin?.label).toBe('Kubernetes scheduler')
    expect(row.pods.map((p) => p.name)).toEqual(['pg-a-1'])
    expect(buildCNPGFleet(base).rows[0].problems.some((p) => p.subject.name === 'pg-a-2-join-x1')).toBe(false)
  })

  it('reads partial coverage by allowed namespaces and treats unnamed partial coverage as unknown', () => {
    const named = buildCNPGFleet(resp({ clusters: [cluster('pg-a', 'db')] }, { coverage: { backups: { state: 'partial', allowedNamespaces: ['db'] } } }))
    expect(named.rows[0].protection.lastSuccessfulBackup.text).toBe('No successful backup yet')
    const unnamed = buildCNPGFleet(resp({ clusters: [cluster('pg-a', 'db')] }, { coverage: { backups: { state: 'partial' } } }))
    expect(unnamed.rows[0].protection.lastSuccessfulBackup.text).toBe('Backups not read in db')
  })

  it('names the cause of a partial read only when the server names the namespace', () => {
    const denied = buildCNPGFleet(resp({ clusters: [cluster('pg-a', 'db')] }, { coverage: { backups: { state: 'partial', allowedNamespaces: ['other'], deniedNamespaces: ['db'] } } }))
    expect(denied.rows[0].protection.lastSuccessfulBackup.text).toBe('No access to Backups')
    const uncached = buildCNPGFleet(resp({ clusters: [cluster('pg-a', 'db')] }, { coverage: { pods: { state: 'partial', allowedNamespaces: ['other'], uncachedNamespaces: ['db'] } } }))
    expect(uncached.rows[0].replication.text).toBe('Radar does not cache Pods in db')
    const none = buildCNPGFleet(resp({ clusters: [cluster('pg-a', 'db')] }, { coverage: { pods: { state: 'uncached' } } }))
    expect(none.rows[0].replication.text).toBe('Radar does not cache Pods')
    const mixed = buildCNPGFleet(resp({ clusters: [cluster('pg-a', 'db')] }, { coverage: { pods: { state: 'uncached', uncachedNamespaces: ['other'], deniedNamespaces: ['db'] } } }))
    expect(mixed.rows[0].replication.text).toBe('No access to Pods')
  })

  it('says no access instead of "no replica pods" when Pods are unreadable', () => {
    const fleet = buildCNPGFleet(resp({ clusters: [cluster('pg-a', 'db')] }, { coverage: { pods: { state: 'denied' } } }))
    expect(fleet.rows[0].replication.text).toBe('No access to Pods')
  })

  it('does not report the recovery window or last backup as absent when ObjectStores are unreadable', () => {
    const c = cluster('pg-a', 'db', { spec: { plugins: [{ name: 'barman-cloud.cloudnative-pg.io', parameters: { barmanObjectName: 'store' } }] } })
    const p = buildCNPGFleet(resp({ clusters: [c] }, { coverage: { objectStores: { state: 'denied' } } })).rows[0].protection
    expect(p.recoveryWindow.text).toBe('No access to ObjectStores')
    expect(p.lastSuccessfulBackup.text).toBe('No access to ObjectStores')
  })

  it('ignores recovery sources from other plugins that reuse the barman parameter names', () => {
    const src = cluster('pg-a', 'db', { spec: { plugins: [{ name: 'barman-cloud.cloudnative-pg.io', parameters: { barmanObjectName: 'store' } }] } })
    const other = cluster('pg-x', 'db', {
      spec: {
        bootstrap: { recovery: { source: 'origin' } },
        externalClusters: [{ name: 'origin', plugin: { name: 'some-other-plugin', parameters: { barmanObjectName: 'store', serverName: 'pg-a' } } }],
      },
    })
    const row = buildCNPGFleet(resp({ clusters: [src, other] })).rows.find((r) => r.name === 'pg-a')!
    expect(row.protection.restoreValidation.text).toBe('None recorded')
  })

  it('words unreadable ObjectStores by coverage state', () => {
    const c = cluster('pg-a', 'db', { spec: { plugins: [{ name: 'barman-cloud.cloudnative-pg.io', parameters: { barmanObjectName: 'store' } }] } })
    const p = buildCNPGFleet(resp({ clusters: [c] }, { coverage: { objectStores: { state: 'syncing' } } })).rows[0].protection
    expect(p.lastSuccessfulBackup.text).toBe('Loading…')
    expect(p.recoveryWindow.text).toBe('Loading…')
  })

  it('shows the Pods’ ready count and raises an availability problem when status claims more', () => {
    const fleet = buildCNPGFleet(
      resp({
        clusters: [cluster('pg-a', 'db')],
        pods: [pod('pg-a-1', 'db', 'pg-a', 'primary', false), pod('pg-a-2', 'db', 'pg-a', 'replica'), pod('pg-a-3', 'db', 'pg-a', 'replica', false)],
      }, { issues: [serverProblem('CNPGInstanceReadinessMismatch', '2 of 3 instance Pods not ready, including the primary', 'Cluster', 'pg-a', 'critical')] }),
    )
    const row = fleet.rows[0]
    expect(row.podReadiness).toEqual({ ready: 1, total: 3 })
    expect(row.readinessContradicted).toBe(true)
    expect(cnpgReadyInstances(row)).toMatchObject({ text: '1/3', tone: 'degraded' })
    expect(cnpgReadyInstances(row).note).toContain('CNPG status reports 3 ready')
    const problem = row.problems[0]
    expect(problem.severity).toBe('critical')
    expect(problem.category).toBe('availability')
    expect(problem.title).toBe('2 of 3 instance Pods not ready, including the primary')
    expect(row.attention).toBe(true)
    expect(fleet.attentionCount).toBe(1)
  })

  it('agrees with status when the Pods do, and never judges unreadable Pods', () => {
    const agreeing = buildCNPGFleet(
      resp({
        clusters: [cluster('pg-a', 'db', { status: { readyInstances: 2 } })],
        pods: [pod('pg-a-1', 'db', 'pg-a', 'primary'), pod('pg-a-2', 'db', 'pg-a', 'replica'), pod('pg-a-3', 'db', 'pg-a', 'replica', false)],
      }),
    ).rows[0]
    expect(agreeing.readinessContradicted).toBeUndefined()
    expect(cnpgReadyInstances(agreeing)).toEqual({ text: '2/3' })
    const denied = buildCNPGFleet(resp({ clusters: [cluster('pg-a', 'db')] }, { coverage: { pods: { state: 'denied' } } })).rows[0]
    expect(denied.podReadiness).toBeUndefined()
    expect(denied.problems).toEqual([])
  })

  it('separates missing operator readiness from an observed Pod count', () => {
    const instances = { ready: null, desired: 1 }
    expect(cnpgReadyInstances({ instances, podReadiness: { ready: 0, total: 0 } })).toEqual({
      text: 'Not reported by the operator', podText: '0 of 1 instance Pods ready',
    })
    expect(cnpgReadyInstances({ instances })).toEqual({ text: 'Not reported by the operator', podText: undefined })
    expect(cnpgReadyInstances({ instances: { ready: null, desired: null }, podReadiness: { ready: 1, total: 1 } }).podText).toBe('1 instance Pod ready')
    expect(cnpgReadyInstances({ instances: { ready: 0, desired: 1 } })).toEqual({ text: '0/1' })
  })

  it('names both primaries when status and the role label disagree', () => {
    const row = buildCNPGFleet(
      resp({
        clusters: [cluster('pg-a', 'db', { status: { currentPrimary: 'pg-a-2', readyInstances: 1 } })],
        pods: [pod('pg-a-1', 'db', 'pg-a', 'primary'), pod('pg-a-2', 'db', 'pg-a', 'replica', false)],
      }, { issues: [serverProblem('CNPGPrimaryLabelMismatch', 'CNPG status names pg-a-2 primary; the Pod labelled primary is pg-a-1')] }),
    ).rows[0]
    expect(row.primaryConflict).toEqual({ status: 'pg-a-2', labelled: 'pg-a-1' })
    expect(row.problems.map((p) => p.title)).toContain('CNPG status names pg-a-2 primary; the Pod labelled primary is pg-a-1')
  })
})

describe('schedule fact', () => {
  it('reads the schedule in words when the server supplied a reading, with the cron as its source', () => {
    const sched = { metadata: { namespace: 'db', name: 'nightly' }, spec: { cluster: { name: 'pg-a' }, schedule: '0 0 2 * * *' } }
    const withReading = buildCNPGFleet(resp({ clusters: [cluster('pg-a', 'db')], scheduledBackups: [sched] }, { scheduleReadings: { 'db/nightly': 'every day at 02:00 UTC' } }))
    expect(withReading.rows[0].protection.schedule).toMatchObject({ text: 'Enabled · every day at 02:00 UTC · blocked: no backup destination', source: 'ScheduledBackup nightly · cron 0 0 2 * * *' })
    const without = buildCNPGFleet(resp({ clusters: [cluster('pg-a', 'db')], scheduledBackups: [sched] }))
    expect(without.rows[0].protection.schedule.text).toBe('Enabled · 0 0 2 * * * · blocked: no backup destination')
  })
})

 describe('CNPG certainty', () => {
  const archiving = cluster('pg', 'db', { spec: { backup: { barmanObjectStore: { destinationPath: 's3://backups' } } }, status: { conditions: [{ type: 'ContinuousArchiving', status: 'True' }] } })
  it('degrades an archiving cluster with no completed Backup and names the read', () => {
    const p = buildCNPGFleet(resp({ clusters: [archiving] })).rows[0].protection
    expect(p.lastSuccessfulBackup).toMatchObject({ tone: 'degraded', text: 'No successful backup yet', source: 'Backups read in this namespace; none completed' })
  })
  it('keeps unread Backups unknown', () => {
    const p = buildCNPGFleet(resp({ clusters: [archiving] }, { coverage: { backups: { state: 'denied' } } })).rows[0].protection
    expect(p.lastSuccessfulBackup).toMatchObject({ tone: 'unknown', source: 'Backups not read' })
  })
  const declaration = (applied: boolean, observed = 2) => ({ apiVersion: G, kind: 'Database', metadata: { name: 'app', namespace: 'db', generation: 2 }, spec: { cluster: { name: 'pg' } }, status: { applied, observedGeneration: observed } })
  it('shows a qualified lower bound inline when a declaration kind was not read', () => {
    const d = buildCNPGFleet(resp({ clusters: [archiving], databases: [declaration(true)] }, { coverage: { publications: { state: 'denied' } } })).rows[0].declarations
    expect(d.summary).toEqual({ text: '≥1 reconciled; Publications not read', tone: 'unknown' })
  })
  it('does not use an exact denominator when some declarations were not read', () => {
    const d = buildCNPGFleet(resp({ clusters: [archiving], databases: [declaration(false)] }, { coverage: { subscriptions: { state: 'denied' } } })).rows[0].declarations
    expect(d.summary.text).toBe('≥1 not reconciled; Subscriptions not read')
    expect(d.summary.tone).toBe('degraded')
  })
  it('counts stale success and failure as pending in the fleet', () => {
    for (const applied of [true, false]) {
      const d = buildCNPGFleet(resp({ clusters: [archiving], databases: [declaration(applied, 1)] })).rows[0].declarations
      expect(d).toMatchObject({ total: 1, pending: 1, failed: 0, summary: { text: '1 of 1 pending', tone: 'unknown' } })
    }
  })
})

it('uses plain WAL evidence without turning an operator success into an archive', () => {
  const condition = { type: 'ContinuousArchiving', status: 'True', message: 'Continuous archiving is working', lastTransitionTime: '2026-10-01T12:00:00Z' }
  const c = cluster('payments', 'db', { status: { conditions: [condition] } })
  const p = buildCNPGFleet(resp({ clusters: [c] })).rows[0].protection
  expect(p.walArchiving).toMatchObject({ text: 'Not archived: no destination configured', operatorCondition: condition })
  expect(p.walArchiving.detail).toBe("WAL is not archived to recovery storage, so point-in-time recovery is unavailable. CloudNativePG still reports archiving as working because, with no destination, it accepts each WAL file without keeping it.")
  expect(p.recoveryWindow.text).toBe('None: no backup destination')
  c.spec.backup = { barmanObjectStore: { destinationPath: 's3://backups' } }
  const configured = buildCNPGFleet(resp({ clusters: [c] })).rows[0].protection.walArchiving
  expect(configured).toMatchObject({ tone: 'healthy', source: 'Cluster status · ContinuousArchiving=True' })
  expect(configured.text).toBe('CNPG reports archiving')
})
it('uses the schedule method blocker in plain recovery summaries, including a mismatched destination', () => {
  const c = cluster('payments', 'db')
  const schedule = { metadata: { name: 'nightly', namespace: 'db' }, spec: { method: 'barmanObjectStore', cluster: { name: 'payments' }, schedule: '0 0 2 * * *' } }
  const data = resp({ clusters: [c], scheduledBackups: [schedule] }, { scheduleReadings: { 'db/nightly': 'every day at 02:00 UTC' } })
  const fact = () => buildCNPGFleet(data).rows[0].protection.schedule
  expect(fact()).toMatchObject({ text: 'Enabled · every day at 02:00 UTC · blocked: no backup destination', tone: 'degraded' })
  c.spec.backup = { volumeSnapshot: {} }
  expect(fact()).toMatchObject({ text: 'Enabled · every day at 02:00 UTC · blocked: no barmanObjectStore destination', tone: 'degraded' })
  schedule.spec.method = 'volumeSnapshot'
  expect(fact()).toMatchObject({ text: 'Enabled · not run yet', tone: 'neutral' })
  delete data.scheduleReadings
  expect(fact().text).toBe('Enabled · not run yet')
  expect(fact().source).toBe('ScheduledBackup nightly · cron 0 0 2 * * *')
  delete (schedule.spec as any).schedule
  expect(fact().text).toBe('Enabled · not run yet')
  data.objects.backups = [{ apiVersion: 'postgresql.cnpg.io/v1', kind: 'Backup', metadata: { namespace: 'db', name: 'run', labels: { 'cnpg.io/scheduled-backup': 'nightly' } }, spec: { cluster: { name: 'payments' } }, status: { phase: 'completed' } }]
  expect(fact().text).toBe('Enabled')
})

it('keeps the plain scheduler cause and the complete scheduler message as separate evidence', () => {
  const pending = { apiVersion: 'v1', kind: 'Pod', metadata: { name: 'orders-2-join-x', namespace: 'db', labels: { 'cnpg.io/cluster': 'orders', 'cnpg.io/jobRole': 'join', 'cnpg.io/instanceName': 'orders-2' } } }
  const raw = '0/2 nodes are available: 2 Too many pods. preemption: no victims.'
  const data = { ...resp({ clusters: [cluster('orders', 'db')] }, { issues: [{ id: 'j', severity: 'critical' as const, category: 'unschedulable', kind: 'Pod', namespace: 'db', name: 'orders-2-join-x', reason: 'Unschedulable', message: raw }] }), jobPods: [pending] }
  const problem = buildCNPGFleet(data).rows[0].problems.find((p) => p.subject.name === 'orders-2-join-x')!
  expect(problem.instance).toBe('orders-2')
  expect(problem.detail).toBe('both nodes have reached their Pod limit')
  expect(problem.rawDetail).toBe(raw)
})

it.each(['False', 'Unknown', undefined])('does not invent operator archiving success for %s', (status) => {
  const c = cluster('analytics', 'db', { status: { conditions: status ? [{ type: 'ContinuousArchiving', status }] : [] } })
  expect(buildCNPGFleet(resp({ clusters: [c] })).rows[0].protection.walArchiving.detail).toBe('WAL is not archived to recovery storage, so point-in-time recovery is unavailable.')
})
it('counts zero ready instance Pods only when the Pod inventory was read', () => {
  const c = cluster('analytics', 'db', { spec: { instances: 1 }, status: { readyInstances: undefined } })
  expect(buildCNPGFleet(resp({ clusters: [c], pods: [] })).rows[0].podReadiness).toEqual({ ready: 0, total: 0 })
  expect(buildCNPGFleet(resp({ clusters: [c] }, { coverage: { pods: { state: 'denied' } } })).rows[0].podReadiness).toBeUndefined()
})

it('counts an enabled destination-blocked schedule as a Cluster protection problem', () => {
  const c = cluster('payments', 'db', { spec: { instances: 1 }, status: { readyInstances: 1, phase: 'Cluster in healthy state' } })
  const schedule = { apiVersion: G, kind: 'ScheduledBackup', metadata: { name: 'payments-nightly', namespace: 'db' }, spec: { cluster: { name: 'payments' } } }
  const fleet = buildCNPGFleet(resp({ clusters: [c], scheduledBackups: [schedule] }, { issues: [serverProblem('CNPGScheduleDestinationMissing', 'Backup schedule payments-nightly cannot run: no backup destination', 'ScheduledBackup', 'payments-nightly')] }))
  const row = fleet.rows[0]
  expect(row.problems).toContainEqual(expect.objectContaining({ title: 'Backup schedule payments-nightly cannot run: no backup destination', severity: 'warning', category: 'protection', subject: expect.objectContaining({ kind: 'ScheduledBackup', name: 'payments-nightly' }) }))
  expect(row.attention).toBe(true)
  expect(fleet.attentionCount).toBe(1)
  expect(fleet.categoryCounts.protection).toBe(1)
  expect(row.categories.has('protection')).toBe(true)
  expect(cnpgDimensions({ row }).find((d) => d.id === 'protection')?.tone).toBe('degraded')
  for (const schedules of [[], [{ ...schedule, spec: { ...schedule.spec, suspend: true } }]]) {
    expect(buildCNPGFleet(resp({ clusters: [c], scheduledBackups: schedules })).rows[0].attention).toBe(false)
  }
  expect(buildCNPGFleet(resp({ clusters: [c], scheduledBackups: [schedule] }, { coverage: { scheduledBackups: { state: 'denied' } } })).attentionCount).toBe(0)
})

it('marks a schedule-method mismatch even when the Cluster has another working destination', () => {
  const c = cluster('payments', 'db', { spec: { instances: 1, plugins: [{ name: 'barman-cloud.cloudnative-pg.io', isWALArchiver: true, parameters: { barmanObjectName: 'store' } }] }, status: { readyInstances: 1, conditions: [{ type: 'ContinuousArchiving', status: 'True' }] } })
  const schedule = { metadata: { name: 'payments-nightly', namespace: 'db' }, spec: { cluster: { name: 'payments' }, method: 'barmanObjectStore' } }
  const row = buildCNPGFleet(resp({ clusters: [c], scheduledBackups: [schedule] }, { issues: [serverProblem('CNPGScheduleDestinationMissing', 'Backup schedule payments-nightly cannot run: no barmanObjectStore destination', 'ScheduledBackup', 'payments-nightly')] })).rows[0]
  expect(row.attention).toBe(true)
  expect(cnpgDimensions({ row }).find((d) => d.id === 'protection')).toMatchObject({ tone: 'degraded', text: 'Backup schedule payments-nightly cannot run: no barmanObjectStore destination' })
})


it('uses server findings without classifying cached-object problems again', () => {
  const c = cluster('pg-a', 'db', { status: { currentPrimary: 'pg-a-2' } })
  const schedule = { metadata: { name: 'nightly', namespace: 'db' }, spec: { cluster: { name: 'pg-a' } } }
  const row = buildCNPGFleet(resp({ clusters: [c], pods: [pod('pg-a-1', 'db', 'pg-a', 'primary', false)], scheduledBackups: [schedule] })).rows[0]
  expect(row.readinessContradicted).toBe(true)
  expect(row.primaryConflict).toEqual({ status: 'pg-a-2', labelled: 'pg-a-1' })
  expect(row.problems).toEqual([])
})


it('excludes predecessor backup success, failures and restores from a recreated Cluster', () => {
  const c = cluster('pg-a', 'db', { metadata: { uid: 'current', creationTimestamp: '2026-10-01T00:00:00Z' }, spec: { plugins: [{ name: 'barman-cloud.cloudnative-pg.io', isWALArchiver: true, parameters: { barmanObjectName: 'store' } }] } })
  const old = { apiVersion: G, kind: 'Backup', metadata: { name: 'old', namespace: 'db', creationTimestamp: '2026-10-01T01:00:00Z' }, spec: { cluster: { name: 'pg-a' } }, status: { phase: 'completed', startedAt: '2026-10-01T01:00:00Z', stoppedAt: '2026-10-01T01:01:00Z', pluginMetadata: { clusterUID: 'previous' } } }
  const failed = { ...old, metadata: { ...old.metadata, name: 'old-failed' }, status: { ...old.status, phase: 'failed' } }
  const restored = cluster('restored', 'db', { spec: { bootstrap: { recovery: { backup: { name: 'old' } } } } })
  const store = { apiVersion: 'barmancloud.cnpg.io/v1', kind: 'ObjectStore', metadata: { name: 'store', namespace: 'db' }, status: { serverRecoveryWindow: { 'pg-a': { lastSuccessfulBackupTime: '2026-09-30T23:00:00Z' } } } }
  const data = resp({ clusters: [c, restored], backups: [old, failed], objectStores: [store] }, { issues: [serverProblem('CNPGBackupFailed', 'Previous backup failed', 'Backup', 'old-failed')] })
  const row = buildCNPGFleet(data).rows.find((r) => r.name === 'pg-a')!
  expect(row.protection.lastSuccessfulBackup.text).toBe('No successful backup yet')
  expect(row.protection.restoreValidation.text).toBe('None recorded')
  expect(row.problems.some((p) => p.subject.name === 'old-failed')).toBe(false)
  const current = { ...old, status: { ...old.status, pluginMetadata: { clusterUID: 'current' } } }
  expect(buildCNPGFleet({ ...data, objects: { ...data.objects, backups: [current, failed] } }).rows.find((r) => r.name === 'pg-a')!.protection.lastSuccessfulBackup).toMatchObject({ text: 'Completed', source: 'Backup old' })
})

it('does not attribute predecessor plugin restores or notes to a recreated source', () => {
  const source = cluster('pg-a', 'db', { metadata: { uid: 'current', creationTimestamp: '2026-10-01T00:00:00Z' }, spec: { plugins: [{ name: 'barman-cloud.cloudnative-pg.io', parameters: { barmanObjectName: 'store', serverName: 'archive' } }] } })
  const restored = cluster('restored', 'db', {
    metadata: { uid: 'restored', creationTimestamp: '2026-10-02T00:00:00Z' },
    spec: { bootstrap: { recovery: { source: 'origin' } }, externalClusters: [{ name: 'origin', plugin: { name: 'barman-cloud.cloudnative-pg.io', parameters: { barmanObjectName: 'store', serverName: 'archive' } } }] },
  })
  const fact = (target: any, backups: any[] = []) => buildCNPGFleet(resp({ clusters: [source, target], backups })).rows.find((r) => r.name === 'pg-a')!.protection.restoreValidation
  const beforeSource = { ...restored, metadata: { ...restored.metadata, creationTimestamp: '2026-09-30T00:00:00Z' } }
  expect(cnpgRecoveryMatchesCluster(beforeSource, source, [])).toBe(false)
  expect(fact(beforeSource).text).toBe('None recorded')
  const pinned = { ...restored, spec: { ...restored.spec, bootstrap: { recovery: { source: 'origin', recoveryTarget: { backupID: 'old-id' } } } } }
  const oldBackup = { apiVersion: G, kind: 'Backup', metadata: { name: 'old', namespace: 'db' }, spec: { cluster: { name: 'pg-a' } }, status: { backupId: 'old-id', pluginMetadata: { clusterUID: 'previous' } } }
  expect(cnpgRecoveryMatchesCluster(pinned, source, [oldBackup])).toBe(false)
  expect(fact(pinned, [oldBackup]).text).toBe('None recorded')
  const pitr = { ...restored, spec: { ...restored.spec, bootstrap: { recovery: { source: 'origin', recoveryTarget: { targetTime: '2026-09-30T23:00:00Z' } } } } }
  expect(fact(pitr).text).toBe('None recorded')
  const withNote = (sourceUID?: string, targetUID = 'restored') => ({ ...restored, metadata: { ...restored.metadata, annotations: { 'radar.skyhook.io/restore-validation': JSON.stringify({ recordedAt: '2026-10-02T02:00:00Z', checked: 'application data', source: { namespace: 'db', name: 'pg-a', uid: sourceUID, verified: !!sourceUID }, target: { namespace: 'db', name: 'restored', uid: targetUID, verified: true } }) } } })
  expect(fact(withNote('previous')).text).toBe('None recorded')
  expect(fact(withNote()).text).toBe('Archive restored into restored')
  expect(fact(withNote('current')).text).toBe('Validation recorded')
  const copied = withNote('current', 'previous-target')
  expect(getCNPGRestoreValidation(copied)).toBeNull()
  expect(fact(copied).text).toBe('Archive restored into restored')
  expect(fact(restored).source).toContain('same archive currently configured')
  expect(fact({ ...restored, status: { readyInstances: 0 } }).text).toBe('Archive recovery declared in restored')
  const currentBackup = { ...oldBackup, status: { ...oldBackup.status, pluginMetadata: { clusterUID: 'current' } } }
  expect(cnpgRecoveryMatchesCluster(pinned, source, [currentBackup])).toBe(true)
})

it('matches in-tree recovery by the actual archive and endpoint rather than server name alone', () => {
  const archive = { destinationPath: 's3://bucket/prefix/', endpointURL: 'https://s3.example/', serverName: 'pg-a' }
  const source = cluster('pg-a', 'db', { spec: { backup: { barmanObjectStore: archive } } })
  const restored = cluster('restored', 'db', { spec: { bootstrap: { recovery: { source: 'origin' } }, externalClusters: [{ name: 'origin', barmanObjectStore: { ...archive, destinationPath: 's3://bucket/prefix' } }] } })
  expect(cnpgRecoveryMatchesCluster(restored, source, [])).toBe(true)
  const other = { ...source, spec: { backup: { barmanObjectStore: { ...archive, endpointURL: 'https://other.example' } } } }
  expect(cnpgRecoveryMatchesCluster(restored, other, [])).toBe(false)
})
