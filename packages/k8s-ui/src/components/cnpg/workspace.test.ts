import { describe, it, expect } from 'vitest'
import { buildCNPGFleet, type CNPGWorkspaceResponse, type CNPGWorkspaceKey, CNPG_WORKSPACE_KEYS } from './workspace'

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
    expect(row.replication.text).toContain('2/2 replicas ready')
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
    expect(fleet.rows[0].protection.lastSuccessfulBackup.text).toBe('None observed')
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

  it('reads partial coverage by allowed namespaces and treats unnamed partial coverage as unknown', () => {
    const named = buildCNPGFleet(resp({ clusters: [cluster('pg-a', 'db')] }, { coverage: { backups: { state: 'partial', allowedNamespaces: ['db'] } } }))
    expect(named.rows[0].protection.lastSuccessfulBackup.text).toBe('None observed')
    const unnamed = buildCNPGFleet(resp({ clusters: [cluster('pg-a', 'db')] }, { coverage: { backups: { state: 'partial' } } }))
    expect(unnamed.rows[0].protection.lastSuccessfulBackup.text).toBe('No access to Backups')
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
})
