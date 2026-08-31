import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import {
  getCNPGScheduledBackupStatus,
  getCNPGScheduledBackupNextSchedule,
  getCNPGClusterCertificateExpirations,
  getCNPGClusterStatus,
  getCNPGBackupStatus,
  getCNPGPoolerStatus,
  isCNPGPoolerPaused,
  getCNPGVolumeHealth,
  getCNPGClusterInstancesReportedState,
  getCNPGClusterBackupConfig,
  getCNPGClusterBarmanPlugin,
  getCNPGWALArchivingFailure,
  getCNPGLastBackupFailure,
  classifyCNPGClusterPhase,
  classifyCNPGBackupPhase,
  CNPG_CLUSTER_PHASES_HEALTHY,
  CNPG_CLUSTER_PHASES_TRANSIENT,
  CNPG_CLUSTER_PHASES_FAILING,
  CNPG_CLUSTER_PHASES_TERMINAL,
  CNPG_CLUSTER_PHASES_ATTENTION,
  CNPG_BACKUP_PHASES_TRANSIENT,
  CNPG_BARMAN_PLUGIN_NAME,
  CNPG_GROUP,
  isApiGroup,
  getCNPGClusterDisplayState,
  getCNPGClusterInstances,
  getCNPGPoolerInstances,
  getCNPGClusterIsReplica,
  getCNPGClusterReplicaSource,
} from './resource-utils-cnpg'
import { getCellFilterValue } from './resource-utils'

describe('getCNPGClusterCertificateExpirations', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-04-28T12:00:00Z'))
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  it('regression: future Go-format dates are not flagged as expired (issue #554)', () => {
    const resource = {
      status: {
        certificates: {
          expirations: {
            'mycluster-ca': '2026-07-27 08:27:41 +0000 UTC',
            'mycluster-server': '2026-07-27 08:27:41 +0000 UTC',
            'mycluster-replication': '2026-07-27 08:27:41 +0000 UTC',
          },
        },
      },
    }
    const certs = getCNPGClusterCertificateExpirations(resource)
    expect(certs).toHaveLength(3)
    for (const cert of certs) {
      expect(cert.daysUntilExpiry).toBeGreaterThan(0)
      expect(cert.daysUntilExpiry).toBeLessThanOrEqual(91)
    }
  })

  it('floors fractional days down so threshold banners do not misfire', () => {
    // Pinned now: 2026-04-28T12:00:00Z; expiry 2026-07-27T00:00:00Z is 89.5
    // days away. Math.floor pins this to 89, not 90 — locks day-boundary
    // semantics so a future Math.ceil/round refactor doesn't shift the
    // <30d / <7d alert thresholds.
    const resource = {
      status: {
        certificates: {
          expirations: { 'mycluster-ca': '2026-07-27 00:00:00 +0000 UTC' },
        },
      },
    }
    const [cert] = getCNPGClusterCertificateExpirations(resource)
    expect(cert.daysUntilExpiry).toBe(89)
  })

  it('flags genuinely expired certificates as negative', () => {
    const resource = {
      status: {
        certificates: {
          expirations: {
            'mycluster-ca': '2026-04-27 08:27:41 +0000 UTC',
          },
        },
      },
    }
    const [cert] = getCNPGClusterCertificateExpirations(resource)
    expect(cert.daysUntilExpiry).toBeLessThan(0)
  })

  it('maps unparseable values to the -1 sentinel (renders as "expired")', () => {
    const resource = {
      status: { certificates: { expirations: { 'mycluster-ca': 'garbage' } } },
    }
    const [cert] = getCNPGClusterCertificateExpirations(resource)
    expect(cert.daysUntilExpiry).toBe(-1)
  })

  it('returns empty list when no expirations are present', () => {
    expect(getCNPGClusterCertificateExpirations({})).toEqual([])
    expect(getCNPGClusterCertificateExpirations({ status: {} })).toEqual([])
  })
})

// ============================================================================
// PHASE TAXONOMY
// ============================================================================

const cluster = (status: any = {}, spec: any = {}) => ({ spec, status })

describe('classifyCNPGClusterPhase', () => {
  it('buckets every phase constant shipped by CNPG 1.27', () => {
    for (const p of CNPG_CLUSTER_PHASES_HEALTHY) expect(classifyCNPGClusterPhase(p)).toBe('healthy')
    for (const p of CNPG_CLUSTER_PHASES_TRANSIENT) expect(classifyCNPGClusterPhase(p)).toBe('transient')
    for (const p of CNPG_CLUSTER_PHASES_FAILING) expect(classifyCNPGClusterPhase(p)).toBe('failing')
    for (const p of CNPG_CLUSTER_PHASES_TERMINAL) expect(classifyCNPGClusterPhase(p)).toBe('terminal')
    for (const p of CNPG_CLUSTER_PHASES_ATTENTION) expect(classifyCNPGClusterPhase(p)).toBe('attention')
  })

  it('matches on equality, not substring', () => {
    // The bug this pins: "Upgrading Postgres major version" contains neither
    // "Upgrading cluster" nor vice versa, but a sloppy includes() over the
    // transient list would still have to enumerate it. Both must classify.
    expect(classifyCNPGClusterPhase('Upgrading cluster')).toBe('transient')
    expect(classifyCNPGClusterPhase('Upgrading Postgres major version')).toBe('transient')
    // A phase that merely CONTAINS a known one must not inherit its bucket.
    expect(classifyCNPGClusterPhase('Cluster in healthy state (probably)')).toBe('unknown')
  })

  it('classifies an unknown phase from a future CNPG minor as unknown', () => {
    expect(classifyCNPGClusterPhase('Reticulating splines')).toBe('unknown')
    expect(classifyCNPGClusterPhase('')).toBe('unknown')
  })

  it('has no invented entries — every listed phase is a real CNPG constant', () => {
    // "Creating replica" was the shipped typo; CNPG emits "Creating a new replica".
    expect(CNPG_CLUSTER_PHASES_TRANSIENT).toContain('Creating a new replica')
    expect(CNPG_CLUSTER_PHASES_TRANSIENT).not.toContain('Creating replica')
    // PhaseDefinitionInvalid is absent from 1.27 and 1.28 but present upstream
    // after 1.28. Matching is by equality, so carrying it is inert against an
    // operator that never emits it, and correct once one does.
    expect(CNPG_CLUSTER_PHASES_TERMINAL).toContain('Invalid cluster definition')
    expect(classifyCNPGClusterPhase('Invalid cluster definition')).toBe('terminal')
  })
})

describe('getCNPGClusterStatus', () => {
  it('renders an unrecoverable cluster red even when all pods are ready', () => {
    // The shipped bug: readiness was checked first, so this fell through every
    // phase list and landed on the neutral-grey fallback.
    const s = getCNPGClusterStatus(cluster(
      { phase: 'Cluster is unrecoverable and needs manual intervention', readyInstances: 3 },
      { instances: 3 },
    ))
    expect(s.level).toBe('unhealthy')
    expect(s.text).toBe('Unrecoverable')
  })

  it('renders both plugin-failure phases red', () => {
    for (const phase of [
      'Cluster cannot proceed to reconciliation due to an unknown plugin being required',
      'Cluster cannot proceed to reconciliation due to an error while interacting with plugins',
    ]) {
      expect(getCNPGClusterStatus(cluster({ phase, readyInstances: 2 }, { instances: 2 })).level).toBe('unhealthy')
    }
  })

  it('renders a major-version upgrade as transient, not unknown', () => {
    const s = getCNPGClusterStatus(cluster({ phase: 'Upgrading Postgres major version', readyInstances: 1 }, { instances: 3 }))
    expect(s.level).toBe('degraded')
    expect(s.text).toBe('Major Upgrade')
  })

  it('uses the alert tier for failover, between degraded and unhealthy', () => {
    expect(getCNPGClusterStatus(cluster({ phase: 'Failing over', readyInstances: 2 }, { instances: 3 })).level).toBe('alert')
  })

  it('treats zero ready instances as down regardless of phase', () => {
    const s = getCNPGClusterStatus(cluster({ phase: 'Cluster in healthy state', readyInstances: 0 }, { instances: 3 }))
    expect(s.level).toBe('unhealthy')
    expect(s.text).toBe('Not Ready')
  })

  it('is healthy when the phase is healthy and all instances are ready', () => {
    expect(getCNPGClusterStatus(cluster({ phase: 'Cluster in healthy state', readyInstances: 2 }, { instances: 2 })).level).toBe('healthy')
  })

  it('surfaces an unrecognized phase verbatim at unknown rather than guessing', () => {
    const s = getCNPGClusterStatus(cluster({ phase: 'Some future phase', readyInstances: 2 }, { instances: 2 }))
    expect(s.level).toBe('unknown')
    expect(s.text).toBe('Some future phase')
  })
})

describe('getCNPGBackupStatus', () => {
  it('escalates walArchivingFailing instead of rendering it neutral', () => {
    // The shipped bug: this phase hit the default branch and rendered grey,
    // while it is CNPG telling you the recovery point is drifting.
    const s = getCNPGBackupStatus({ status: { phase: 'walArchivingFailing' } })
    expect(s.level).toBe('unhealthy')
    expect(s.text).toBe('WAL Archiving Failing')
  })

  it('classifies every backup phase constant shipped by CNPG 1.27', () => {
    expect(classifyCNPGBackupPhase('completed')).toBe('healthy')
    expect(classifyCNPGBackupPhase('failed')).toBe('failed')
    expect(classifyCNPGBackupPhase('walArchivingFailing')).toBe('walArchivingFailing')
    for (const p of CNPG_BACKUP_PHASES_TRANSIENT) expect(classifyCNPGBackupPhase(p)).toBe('transient')
  })

  it('handles started and finalizing, which previously fell through to unknown', () => {
    expect(getCNPGBackupStatus({ status: { phase: 'started' } }).level).toBe('degraded')
    expect(getCNPGBackupStatus({ status: { phase: 'finalizing' } }).level).toBe('degraded')
  })

  it('leaves an unrecognized phase at unknown', () => {
    expect(getCNPGBackupStatus({ status: { phase: 'wat' } }).level).toBe('unknown')
    expect(getCNPGBackupStatus({}).level).toBe('unknown')
  })
})

// ============================================================================
// CONDITIONS — WAL ARCHIVING / LAST BACKUP
// ============================================================================

const withConditions = (...conds: any[]) => ({ status: { conditions: conds } })

describe('getCNPGWALArchivingFailure', () => {
  it('fires only when ContinuousArchiving is False', () => {
    expect(getCNPGWALArchivingFailure(withConditions(
      { type: 'ContinuousArchiving', status: 'True', reason: 'ContinuousArchivingSuccess' },
    ))).toBeNull()

    const f = getCNPGWALArchivingFailure(withConditions(
      { type: 'ContinuousArchiving', status: 'False', reason: 'ContinuousArchivingFailing', message: 'boom', lastTransitionTime: '2026-04-28T10:00:00Z' },
    ))
    expect(f?.reason).toBe('ContinuousArchivingFailing')
    expect(f?.lastTransitionTime).toBe('2026-04-28T10:00:00Z')
  })

  it('returns null when the condition is absent', () => {
    expect(getCNPGWALArchivingFailure({})).toBeNull()
    expect(getCNPGWALArchivingFailure(withConditions({ type: 'Ready', status: 'True' }))).toBeNull()
  })
})

describe('getCNPGLastBackupFailure', () => {
  it('ignores the in-flight BackupStarted state', () => {
    // CNPG sets LastBackupSucceeded=False/BackupStarted while a backup is merely
    // starting. Alerting on it would light the banner on every backup run.
    expect(getCNPGLastBackupFailure(withConditions(
      { type: 'LastBackupSucceeded', status: 'False', reason: 'BackupStarted' },
    ))).toBeNull()
  })

  it('fires on a real LastBackupFailed', () => {
    const f = getCNPGLastBackupFailure(withConditions(
      { type: 'LastBackupSucceeded', status: 'False', reason: 'LastBackupFailed', message: 'no credentials' },
    ))
    expect(f?.reason).toBe('LastBackupFailed')
    expect(f?.message).toBe('no credentials')
  })
})

// ============================================================================
// BUG 7 — timeLineID
// ============================================================================

describe('getCNPGClusterInstancesReportedState', () => {
  it('reads CNPG\'s timeLineID spelling (capital L)', () => {
    const state = getCNPGClusterInstancesReportedState({
      status: {
        instancesReportedState: {
          'pg-main-1': { ip: '10.244.0.9', isPrimary: true, timeLineID: 3 },
          'pg-main-2': { ip: '10.244.0.12', isPrimary: false, timeLineID: 3 },
        },
      },
    })
    expect(state).toHaveLength(2)
    expect(state[0]).toMatchObject({ podName: 'pg-main-1', isPrimary: true, timelineID: 3, ip: '10.244.0.9' })
    expect(state[1].timelineID).toBe(3)
  })

  it('does not read the lowercase timelineID spelling, which CNPG never emits', () => {
    const state = getCNPGClusterInstancesReportedState({
      status: { instancesReportedState: { 'pg-1': { isPrimary: true, timelineID: 9 } } },
    })
    expect(state[0].timelineID).toBeUndefined()
  })
})

// ============================================================================
// POOLER
// ============================================================================

describe('getCNPGPoolerStatus', () => {
  it('does not claim readiness from status.instances, which counts scheduled pods', () => {
    // Verified live: a Pooler whose 2 PgBouncer pods are both Pending (0/1
    // ready) still reports status.instances=2. Calling that "Ready" renders a
    // broken Pooler green.
    const s = getCNPGPoolerStatus({ spec: { instances: 2 }, status: { instances: 2 } })
    expect(s.level).toBe('healthy')
    expect(s.text).toBe('Scheduled')
    expect(s.text).not.toBe('Ready')
  })

  it('flags a shortfall in scheduled pods', () => {
    expect(getCNPGPoolerStatus({ spec: { instances: 3 }, status: { instances: 1 } })).toMatchObject({ level: 'degraded' })
    expect(getCNPGPoolerStatus({ spec: { instances: 3 }, status: { instances: 0 } })).toMatchObject({ level: 'unhealthy', text: 'Not Scheduled' })
  })
})

// ============================================================================
// BUG 6 — PLUGIN-AWARE BACKUP
// ============================================================================

describe('getCNPGClusterBarmanPlugin', () => {
  it('detects the barman-cloud plugin and resolves the ObjectStore + server key', () => {
    const p = getCNPGClusterBarmanPlugin({
      metadata: { name: 'pg-main' },
      spec: { plugins: [{ name: CNPG_BARMAN_PLUGIN_NAME, isWALArchiver: true, parameters: { barmanObjectName: 'store-a' } }] },
    })
    expect(p).toMatchObject({ barmanObjectName: 'store-a', isWALArchiver: true, serverName: 'pg-main' })
  })

  it('prefers an explicit serverName parameter over the cluster name', () => {
    const p = getCNPGClusterBarmanPlugin({
      metadata: { name: 'pg-main' },
      spec: { plugins: [{ name: CNPG_BARMAN_PLUGIN_NAME, parameters: { barmanObjectName: 'store-a', serverName: 'custom' } }] },
    })
    expect(p?.serverName).toBe('custom')
  })

  it('ignores an explicitly disabled plugin and unrelated plugins', () => {
    expect(getCNPGClusterBarmanPlugin({
      spec: { plugins: [{ name: CNPG_BARMAN_PLUGIN_NAME, enabled: false }] },
    })).toBeNull()
    expect(getCNPGClusterBarmanPlugin({
      spec: { plugins: [{ name: 'some-other.plugin.io' }] },
    })).toBeNull()
    expect(getCNPGClusterBarmanPlugin({ spec: {} })).toBeNull()
  })
})

describe('getCNPGClusterBackupConfig', () => {
  it('reports configured for a plugin-managed cluster with no in-tree stanza', () => {
    // The shipped bug: plugin-migrated clusters render the whole backup section
    // blank, indistinguishable from "no backups configured".
    const cfg = getCNPGClusterBackupConfig({
      metadata: { name: 'pg-main' },
      spec: { plugins: [{ name: CNPG_BARMAN_PLUGIN_NAME, parameters: { barmanObjectName: 'store-a' } }] },
      status: {},
    })
    expect(cfg.configured).toBe(true)
    expect(cfg.rpoTrackedOnObjectStore).toBe(true)
    expect(cfg.plugin?.barmanObjectName).toBe('store-a')
  })

  it('still reports in-tree config the old way', () => {
    const cfg = getCNPGClusterBackupConfig({
      spec: { backup: { barmanObjectStore: { destinationPath: 's3://b' }, retentionPolicy: '30d' } },
      status: { lastSuccessfulBackup: '2026-04-28T00:00:00Z' },
    })
    expect(cfg).toMatchObject({ configured: true, destinationPath: 's3://b', retentionPolicy: '30d', plugin: null, rpoTrackedOnObjectStore: false })
  })

  it('reports not-configured when neither path is present', () => {
    expect(getCNPGClusterBackupConfig({ spec: {}, status: {} }).configured).toBe(false)
  })
})

describe('isApiGroup', () => {
  it('matches the exact group, not a substring of it', () => {
    expect(isApiGroup('postgresql.cnpg.io/v1', CNPG_GROUP)).toBe(true)
    // A vendor sub-group contains the CNPG group as a substring but is not it.
    expect(isApiGroup('extension.postgresql.cnpg.io/v1', CNPG_GROUP)).toBe(false)
    expect(isApiGroup('barmancloud.cnpg.io/v1', CNPG_GROUP)).toBe(false)
    expect(isApiGroup('backup.velero.io/v1', 'velero.io')).toBe(false)
    expect(isApiGroup('velero.io/v1', 'velero.io')).toBe(true)
  })

  it('rejects core-group and malformed apiVersions', () => {
    expect(isApiGroup('v1', CNPG_GROUP)).toBe(false)
    expect(isApiGroup('/v1', CNPG_GROUP)).toBe(false)
    expect(isApiGroup(undefined, CNPG_GROUP)).toBe(false)
    expect(isApiGroup(null, CNPG_GROUP)).toBe(false)
  })
})

describe('CNPG_CLUSTER_PHASES_ATTENTION', () => {
  it('excludes the operator-driven rollout delay', () => {
    // "Cluster upgrade delayed" is postponed by the operator's own config and
    // requeued — nothing waits on a human.
    expect(CNPG_CLUSTER_PHASES_ATTENTION).not.toContain('Cluster upgrade delayed')
    expect(CNPG_CLUSTER_PHASES_TRANSIENT).toContain('Cluster upgrade delayed')
    expect(classifyCNPGClusterPhase('Cluster upgrade delayed')).toBe('transient')
  })
})

// The badge and the Go issue detector must agree on the same object. These
// fixtures are mirrored in internal/issues/source_cnpg_test.go
// (TestCNPGBadgeAndIssueAgreeOnReadiness) — change one, change both.
describe('badge/issue agreement on instance readiness', () => {
  it('does not render Healthy while the detector reports a shortfall', () => {
    // CNPG sets "Cluster in healthy state" once reconciliation settles, which
    // can be true while instances are still missing. Returning Healthy here put
    // a green badge on a cluster the Go side flags CNPGClusterDegraded.
    const s = getCNPGClusterStatus(cluster({ phase: 'Cluster in healthy state', readyInstances: 1 }, { instances: 3 }))
    expect(s.level).toBe('degraded')
    expect(s.text).toBe('Degraded')
  })

  it('still renders Healthy only when the count is actually met', () => {
    expect(getCNPGClusterStatus(cluster({ phase: 'Cluster in healthy state', readyInstances: 3 }, { instances: 3 })).level).toBe('healthy')
  })

  it('lets a transient phase explain a legitimate shortfall', () => {
    // Go marks these phaseExplained and raises no issue, so the badge must not
    // say "Degraded" either — it shows the phase, at the same amber tier.
    const s = getCNPGClusterStatus(cluster({ phase: 'Creating a new replica', readyInstances: 1 }, { instances: 3 }))
    expect(s.level).toBe('degraded')
    expect(s.text).toBe('Creating Replica')
  })

  it('lets an attention phase explain a shortfall, matching the supervised Go path', () => {
    // Go marks attention phases phaseExplained regardless of update strategy,
    // so a supervised cluster waiting on a human raises no shortfall issue —
    // the badge must show the phase, not "Degraded".
    const s = getCNPGClusterStatus(cluster({ phase: 'Waiting for user action', readyInstances: 1 }, { instances: 3 }))
    expect(s.level).toBe('degraded')
    expect(s.text).toBe('Needs Action')
  })

  it('flags a shortfall under an unrecognized phase, matching the Go fallthrough', () => {
    expect(getCNPGClusterStatus(cluster({ phase: 'Some future phase', readyInstances: 1 }, { instances: 3 })).level).toBe('degraded')
  })
})

// ============================================================================
// DISPLAY STATES — a badge carries a state, the drawer carries the prose
// ============================================================================

describe('getCNPGClusterDisplayState', () => {
  it('maps every mapped phase to something a badge can hold', () => {
    // 127px is the label budget at w-44. The longest state must clear it with
    // real headroom — not the 0.2px kind that has bitten three times.
    const all = [
      ...CNPG_CLUSTER_PHASES_TERMINAL,
      ...CNPG_CLUSTER_PHASES_TRANSIENT,
      ...CNPG_CLUSTER_PHASES_ATTENTION,
    ]
    for (const phase of all) {
      const state = getCNPGClusterDisplayState(phase)
      expect(state, `${phase} has no display state`).not.toBe(phase)
      // Rough proxy for width in a unit test; the real measurement is in the
      // PR. 22 chars ≈ 120px at this typography.
      expect(state.length, `${state} is too long for a badge`).toBeLessThanOrEqual(22)
    }
  })

  it('passes an unmapped phase through verbatim rather than inventing one', () => {
    // A phase from a newer CNPG minor. Inventing a state for a string we have
    // never seen would be worse than showing the operator's words.
    expect(getCNPGClusterDisplayState('Reticulating splines')).toBe('Reticulating splines')
    expect(getCNPGClusterDisplayState('')).toBe('')
  })

  it('collapses both restart phases to one state — a documented loss', () => {
    // You cannot tell from the table which restart mechanism is in play. The
    // exact phase stays in the drawer; splitting them needs ~140px.
    expect(getCNPGClusterDisplayState('Primary instance is being restarted in-place')).toBe('Restarting Primary')
    expect(getCNPGClusterDisplayState('Primary instance is being restarted without a switchover')).toBe('Restarting Primary')
  })

  it('renders the state, not the sentence, on the badge', () => {
    const s = getCNPGClusterStatus(cluster(
      { phase: 'Cluster is unrecoverable and needs manual intervention', readyInstances: 2 },
      { instances: 2 },
    ))
    expect(s.text).toBe('Unrecoverable')
    expect(s.level).toBe('unhealthy')
  })
})

describe('status column filter reads the badge, not the raw phase', () => {
  // Deliberately asserts LITERAL strings rather than comparing against
  // getCNPGClusterStatus(...).text — both sides would call the same reader and
  // the test would pass while both were wrong. These are the strings a user
  // sees on a badge, so the dropdown must offer exactly them.
  const CLUSTER = 'cnpgclusters'

  it('offers the short state, not CNPG prose', () => {
    expect(getCellFilterValue(
      cluster({ phase: 'Cluster is unrecoverable and needs manual intervention', readyInstances: 2 }, { instances: 2 }),
      'status', CLUSTER,
    )).toBe('Unrecoverable')

    expect(getCellFilterValue(
      cluster({ phase: 'Cluster in healthy state', readyInstances: 2 }, { instances: 2 }),
      'status', CLUSTER,
    )).toBe('Healthy')
  })

  it('separates a WAL-archiving failure from healthy — they share a phase', () => {
    // The defect this pins: both clusters report `Cluster in healthy state`,
    // so filtering on the raw phase collapsed them into one option and the
    // headline state of this integration could not be filtered for at all.
    const healthy = cluster({ phase: 'Cluster in healthy state', readyInstances: 2 }, { instances: 2 })
    const walFailing = cluster({
      phase: 'Cluster in healthy state',
      readyInstances: 2,
      conditions: [{ type: 'ContinuousArchiving', status: 'False', reason: 'ContinuousArchivingFailing' }],
    }, { instances: 2 })

    expect(healthy.status.phase).toBe(walFailing.status.phase)
    expect(getCellFilterValue(healthy, 'status', CLUSTER)).toBe('Healthy')
    expect(getCellFilterValue(walFailing, 'status', CLUSTER)).toBe('WAL Archiving Failing')
  })

  it('covers the other three CNPG kinds', () => {
    expect(getCellFilterValue({ status: { phase: 'completed' } }, 'status', 'cnpgbackups')).toBe('Completed')
    expect(getCellFilterValue(
      { apiVersion: 'postgresql.cnpg.io/v1', spec: { suspend: true } }, 'status', 'scheduledbackups',
    )).toBe('Suspended')
    expect(getCellFilterValue(
      { apiVersion: 'postgresql.cnpg.io/v1', spec: { instances: 2 }, status: { instances: 2 } }, 'status', 'poolers',
    )).toBe('Scheduled')
  })

  it('leaves a foreign CRD sharing the plural on the generic path', () => {
    // `poolers`/`scheduledbackups` are bare plurals — another operator could
    // ship them, and it must not be read through a CNPG accessor.
    expect(getCellFilterValue(
      { apiVersion: 'pooling.example.com/v1', status: { phase: 'Running' } }, 'status', 'poolers',
    )).toBe('Running')
  })
})

describe('an unknown phase outranks the Ready condition', () => {
  // The decision this pins: a phase from a newer CNPG minor renders verbatim.
  // A Ready=True condition read first would turn it into a green "Ready" badge
  // — health asserted from a signal that does not establish it.
  const withPhase = (phase: string, ready?: string) => ({
    spec: { instances: 2 },
    status: {
      phase,
      readyInstances: 2,
      ...(ready ? { conditions: [{ type: 'Ready', status: ready, reason: 'ClusterIsReady' }] } : {}),
    },
  })

  it('shows the operator words, not Ready, when Ready=True', () => {
    const s = getCNPGClusterStatus(withPhase('Rebalancing shards across zones', 'True'))
    expect(s.text).toBe('Rebalancing shards across zones')
    expect(s.level).toBe('unknown')
  })

  it('keeps the red when Ready=False, still showing the phase', () => {
    const s = getCNPGClusterStatus(withPhase('Rebalancing shards across zones', 'False'))
    expect(s.text).toBe('Rebalancing shards across zones')
    expect(s.level).toBe('unhealthy')
  })

  it('falls back to the condition only when there is no phase at all', () => {
    expect(getCNPGClusterStatus(withPhase('', 'True')).text).toBe('Ready')
    expect(getCNPGClusterStatus(withPhase('', 'False')).text).toBe('ClusterIsReady')
    expect(getCNPGClusterStatus({ spec: {}, status: {} }).text).toBe('Unknown')
  })
})

describe('an absent count is unknown, never zero', () => {
  // The window this covers is ordinary: between creation and the operator's
  // first status write. Brief, common, and exactly when someone is watching.
  it('Cluster instances cell renders a dash, not 0', () => {
    expect(getCNPGClusterInstances({ spec: { instances: 3 }, status: {} })).toBe('-/3')
    expect(getCNPGClusterInstances({ spec: {}, status: {} })).toBe('-/-')
    expect(getCNPGClusterInstances({ spec: { instances: 3 }, status: { readyInstances: 0 } })).toBe('0/3')
    expect(getCNPGClusterInstances({ spec: { instances: 3 }, status: { readyInstances: 3 } })).toBe('3/3')
  })

  it('does not contradict the badge during that window', () => {
    // The pairing IS the claim: a cell reading 0/3 beside a badge that does not
    // say degraded is the contradiction this integration exists to remove.
    const fresh = { spec: { instances: 3 }, status: { phase: 'Setting up primary' } }
    expect(getCNPGClusterInstances(fresh)).toBe('-/3')
    expect(getCNPGClusterStatus(fresh).level).not.toBe('unhealthy')
  })

  it('Pooler instances cell renders a dash, not 0', () => {
    expect(getCNPGPoolerInstances({ spec: { instances: 2 }, status: {} })).toBe('-/2')
    expect(getCNPGPoolerInstances({ spec: { instances: 2 }, status: { instances: 0 } })).toBe('0/2')
  })

  it('Pooler badge says Unknown, not a red Not Scheduled', () => {
    // Sharper than the cell: reading absence as 0 fabricated a FAILURE, so an
    // unreconciled Pooler went red on a state nobody had reported yet.
    const fresh = { spec: { instances: 2 }, status: {} }
    expect(getCNPGPoolerStatus(fresh)).toMatchObject({ text: 'Unknown', level: 'unknown' })
    expect(getCNPGPoolerStatus({ spec: { instances: 2 }, status: { instances: 0 } }))
      .toMatchObject({ text: 'Not Scheduled', level: 'unhealthy' })
    expect(getCNPGPoolerStatus({ spec: { instances: 2 }, status: { instances: 2 } }))
      .toMatchObject({ text: 'Scheduled', level: 'healthy' })
  })
})

describe('paused Pooler', () => {
  // PgBouncer PAUSE holds client connections instead of serving them, while
  // every pod stays scheduled and Ready. Reading only the pod counts rendered
  // a pooler that serves nothing in healthy green.
  const paused = { spec: { instances: 2, pgbouncer: { paused: true } }, status: { instances: 2 } }

  it('does not read as healthy', () => {
    expect(getCNPGPoolerStatus(paused)).toMatchObject({ text: 'Paused', level: 'degraded' })
  })

  it('treats pausing as intent, not a fault', () => {
    // Same tier as a paused Velero Schedule: amber, not red.
    expect(getCNPGPoolerStatus(paused).level).not.toBe('unhealthy')
  })

  it('lets a real fault outrank the pause', () => {
    const pausedAndDown = { spec: { instances: 2, pgbouncer: { paused: true } }, status: { instances: 0 } }
    expect(getCNPGPoolerStatus(pausedAndDown)).toMatchObject({ text: 'Not Scheduled', level: 'unhealthy' })
  })

  it('only an explicit true pauses', () => {
    expect(getCNPGPoolerStatus({ spec: { instances: 2, pgbouncer: {} }, status: { instances: 2 } }))
      .toMatchObject({ text: 'Scheduled', level: 'healthy' })
    expect(getCNPGPoolerStatus({ spec: { instances: 2, pgbouncer: { paused: false } }, status: { instances: 2 } }))
      .toMatchObject({ text: 'Scheduled', level: 'healthy' })
    expect(isCNPGPoolerPaused({ spec: { pgbouncer: { paused: true } } })).toBe(true)
    expect(isCNPGPoolerPaused({ spec: {} })).toBe(false)
  })
})

describe('volume health', () => {
  // Field names and shape verified against a live CloudNativePG 1.27 CRD and a
  // running cluster: pvcCount is a number, the rest are arrays of PVC names.
  const healthy = {
    status: { pvcCount: 2, healthyPVC: ['pg-healthy-1', 'pg-healthy-2'] },
  }

  it('reads what the operator reports about volumes that exist', () => {
    const h = getCNPGVolumeHealth(healthy)
    expect(h).toMatchObject({ total: 2, healthy: ['pg-healthy-1', 'pg-healthy-2'], unusable: [] })
  })

  it('surfaces an unusable volume, which is why an instance cannot start', () => {
    // Upstream: unusable means a paired volume is missing.
    const h = getCNPGVolumeHealth({ status: { pvcCount: 3, healthyPVC: ['a'], unusablePVC: ['pg-main-3'] } })
    expect(h?.unusable).toEqual(['pg-main-3'])
  })

  it('stays silent when the operator has reported nothing', () => {
    // Absence must not render as "0 problems" — the section is hidden instead,
    // so it never claims a check it did not perform.
    expect(getCNPGVolumeHealth({ status: {} })).toBeNull()
    expect(getCNPGVolumeHealth({})).toBeNull()
  })

  it('ignores malformed entries rather than rendering them', () => {
    const h = getCNPGVolumeHealth({ status: { pvcCount: 1, healthyPVC: ['ok', 42, null] } })
    expect(h?.healthy).toEqual(['ok'])
  })
})

describe('CNPG scheduled backup lateness', () => {
  const at = (ms: number) => new Date(Date.now() + ms).toISOString()
  const MIN = 60 * 1000

  const sb = (suspend: boolean, next?: string, lastScheduleTime?: string) => ({
    spec: { schedule: '0 0 0 * * *', suspend },
    status: { ...(next ? { nextScheduleTime: next } : {}), ...(lastScheduleTime ? { lastScheduleTime } : {}) },
  })

  it('reports a schedule that missed its own next-run time', () => {
    // Ran once, then stopped. Both lastScheduleTime and nextScheduleTime are
    // present, which is exactly the shape that used to read healthy.
    const badge = getCNPGScheduledBackupStatus(sb(false, at(-200 * 24 * 60 * MIN), at(-200 * 24 * 60 * MIN)))
    expect(badge.level).toBe('degraded')
    expect(badge.text).toBe('Overdue')
  })

  it('stays healthy inside the operator reconcile window', () => {
    const badge = getCNPGScheduledBackupStatus(sb(false, at(-1 * MIN), at(-24 * 60 * MIN)))
    expect(badge.level).toBe('healthy')
  })

  it('keeps suspended as a deliberate state, not a missed backup', () => {
    // The operator stops maintaining nextScheduleTime once suspended, so the
    // stale value must not be read as a missed run.
    const badge = getCNPGScheduledBackupStatus(sb(true, at(-200 * 24 * 60 * MIN)))
    expect(badge.text).toBe('Suspended')
  })

  it('never says overdue while the badge still says healthy', () => {
    // The two read from one test. Before this they disagreed: the badge said
    // Active while this field said overdue on the same row.
    const justPastDue = sb(false, at(-1 * MIN), at(-24 * 60 * MIN))
    expect(getCNPGScheduledBackupStatus(justPastDue).level).toBe('healthy')
    expect(getCNPGScheduledBackupNextSchedule(justPastDue)).toBe('due now')

    const longPastDue = sb(false, at(-200 * 24 * 60 * MIN), at(-200 * 24 * 60 * MIN))
    expect(getCNPGScheduledBackupStatus(longPastDue).level).toBe('degraded')
    expect(getCNPGScheduledBackupNextSchedule(longPastDue)).toContain('overdue by')
  })

  it('does not count down to a run that is not coming', () => {
    expect(getCNPGScheduledBackupNextSchedule(sb(true, at(60 * MIN)))).toBe('suspended')
    expect(getCNPGScheduledBackupNextSchedule(sb(true, at(-60 * MIN)))).toBe('suspended')
  })

  it('says nothing when the operator published no next-run time', () => {
    expect(getCNPGScheduledBackupNextSchedule(sb(false))).toBe('-')
    expect(getCNPGScheduledBackupStatus(sb(false, undefined, at(-24 * 60 * MIN))).level).toBe('healthy')
  })
})

describe('getCNPGClusterIsReplica — the live replica-cluster role, per CNPG semantics', () => {
  // CNPG carries replica-cluster state in `spec.replica` (enabled/primary/self/source),
  // and its own Cluster.IsReplica() reads: enabled wins when set; otherwise the
  // cluster is a replica only while (self || name) != primary. Promotion mutates
  // those fields but leaves the stanza in place, so presence alone cannot be the
  // signal.

  it('standalone replica cluster (replica mode enabled) reads as a replica', () => {
    const resource = {
      metadata: { name: 'pg-eu' },
      spec: { replica: { source: 'pg-us', enabled: true } },
    }
    expect(getCNPGClusterIsReplica(resource)).toBe(true)
  })

  it('a promoted standalone cluster (replica.enabled turned off) is no longer a replica', () => {
    const resource = {
      metadata: { name: 'pg-eu' },
      spec: { replica: { source: 'pg-us', enabled: false } },
    }
    expect(getCNPGClusterIsReplica(resource)).toBe(false)
  })

  it('distributed topology: a designated replica (primary names another cluster) reads as a replica', () => {
    const resource = {
      metadata: { name: 'pg-eu' },
      spec: { replica: { source: 'pg-us', primary: 'pg-us', self: 'pg-eu' } },
    }
    expect(getCNPGClusterIsReplica(resource)).toBe(true)
  })

  it('distributed topology: after promotion (primary now names this cluster) it is the primary, stanza still present', () => {
    const resource = {
      metadata: { name: 'pg-eu' },
      spec: { replica: { source: 'pg-us', primary: 'pg-eu', self: 'pg-eu' } },
    }
    expect(getCNPGClusterIsReplica(resource)).toBe(false)
  })

  it('distributed topology without self falls back to the resource name', () => {
    const promoted = {
      metadata: { name: 'pg-eu' },
      spec: { replica: { source: 'pg-us', primary: 'pg-eu' } },
    }
    expect(getCNPGClusterIsReplica(promoted)).toBe(false)

    const stillReplica = {
      metadata: { name: 'pg-eu' },
      spec: { replica: { source: 'pg-us', primary: 'pg-us' } },
    }
    expect(getCNPGClusterIsReplica(stillReplica)).toBe(true)
  })

  it('a bootstrapping standalone replica with only a source (no enabled, no primary) is a replica', () => {
    // The stanza's source is required; before the operator writes enabled, the
    // cluster is still a replica — (self || name) never equals an unset primary.
    const resource = { metadata: { name: 'pg-eu' }, spec: { replica: { source: 'pg-us' } } }
    expect(getCNPGClusterIsReplica(resource)).toBe(true)
  })

  it('an ordinary standalone cluster with no replica stanza is not a replica', () => {
    expect(getCNPGClusterIsReplica({ metadata: { name: 'pg' }, spec: { instances: 3 } })).toBe(false)
  })

  it('getCNPGClusterReplicaSource reads the source from the replica stanza', () => {
    const resource = { metadata: { name: 'pg-eu' }, spec: { replica: { source: 'pg-us', enabled: true } } }
    expect(getCNPGClusterReplicaSource(resource)).toBe('pg-us')
  })
})
