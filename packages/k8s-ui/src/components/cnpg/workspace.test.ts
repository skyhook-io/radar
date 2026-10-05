import { describe, it, expect } from 'vitest'
import { buildCNPGFleet, cnpgReadyInstances, type CNPGWorkspaceResponse, type CNPGWorkspaceKey, CNPG_WORKSPACE_KEYS } from './workspace'

const G = 'postgresql.cnpg.io/v1'

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
    expect(a.protection.restoreValidation.text).toBe('Restored into pg-a-restore')
    expect(a.protection.restoreValidation.tone).toBe('neutral')
    const r = fleet.rows.find((x) => x.name === 'pg-a-restore')!
    expect(r.protection.restoreValidation.text).toBe('None recorded')
    expect(r.protection.restoreValidation.tone).toBe('unknown')
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
    expect(buildCNPGFleet(resp({ clusters: [src, otherUID] })).rows.find((r) => r.name === 'pg-a')!.protection.restoreValidation.text).toBe('Restored into pg-a-restore')

    const malformed = cluster('pg-a-restore', 'db', { metadata: { annotations: { 'radar.skyhook.io/restore-validation': '{not json' } }, spec: restoredSpec })
    expect(buildCNPGFleet(resp({ clusters: [src, malformed] })).rows.find((r) => r.name === 'pg-a')!.protection.restoreValidation.text).toBe('Restored into pg-a-restore')
  })

  it('ends the recovery window at WAL archiving, not at the last base backup', () => {
    const plugin = { plugins: [{ name: 'barman-cloud.cloudnative-pg.io', parameters: { barmanObjectName: 'store' } }] }
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

  it('shows when archiving started working, only when that was recent and after creation', () => {
    const hourAgo = new Date(Date.now() - 3_600_000).toISOString()
    const resumed = cluster('pg-a', 'db', { metadata: { creationTimestamp: '2026-01-01T00:00:00Z' }, status: { conditions: [{ type: 'ContinuousArchiving', status: 'True', lastTransitionTime: hourAgo }] } })
    expect(buildCNPGFleet(resp({ clusters: [resumed] })).rows[0].protection.walArchiving).toMatchObject({ text: 'Archiving', at: hourAgo, atMeaning: 'since' })
    const old = cluster('pg-a', 'db', { metadata: { creationTimestamp: '2026-01-01T00:00:00Z' }, status: { conditions: [{ type: 'ContinuousArchiving', status: 'True', lastTransitionTime: '2026-01-01T00:02:00Z' }] } })
    expect(buildCNPGFleet(resp({ clusters: [old] })).rows[0].protection.walArchiving.at).toBeUndefined()
  })

  it('reports WAL archiving from the condition and unknown when absent', () => {
    const failing = cluster('pg-a', 'db', { status: { conditions: [{ type: 'ContinuousArchiving', status: 'False', message: 'exit status 1' }] } })
    const silent = cluster('pg-b', 'db')
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
    expect(row.problems[0].detail).toBe('2 node(s) insufficient pods (0/2 nodes available)')
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
      }),
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

  it('names both primaries when status and the role label disagree', () => {
    const row = buildCNPGFleet(
      resp({
        clusters: [cluster('pg-a', 'db', { status: { currentPrimary: 'pg-a-2', readyInstances: 1 } })],
        pods: [pod('pg-a-1', 'db', 'pg-a', 'primary'), pod('pg-a-2', 'db', 'pg-a', 'replica', false)],
      }),
    ).rows[0]
    expect(row.primaryConflict).toEqual({ status: 'pg-a-2', labelled: 'pg-a-1' })
    expect(row.problems.map((p) => p.title)).toContain('CNPG status names pg-a-2 primary; the Pod labelled primary is pg-a-1')
  })
})

describe('schedule fact', () => {
  it('reads the schedule in words when the server supplied a reading, with the cron as its source', () => {
    const sched = { metadata: { namespace: 'db', name: 'nightly' }, spec: { cluster: { name: 'pg-a' }, schedule: '0 0 2 * * *' } }
    const withReading = buildCNPGFleet(resp({ clusters: [cluster('pg-a', 'db')], scheduledBackups: [sched] }, { scheduleReadings: { 'db/nightly': 'every day at 02:00 UTC' } }))
    expect(withReading.rows[0].protection.schedule).toMatchObject({ text: 'Scheduled · every day at 02:00 UTC', source: 'ScheduledBackup nightly · cron 0 0 2 * * *' })
    const without = buildCNPGFleet(resp({ clusters: [cluster('pg-a', 'db')], scheduledBackups: [sched] }))
    expect(without.rows[0].protection.schedule.text).toBe('Scheduled · 0 0 2 * * *')
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
