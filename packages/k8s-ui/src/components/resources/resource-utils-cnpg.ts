// CloudNativePG CRD utility functions

import type { StatusBadge } from './resource-utils'
import { healthColors, formatAge, formatDuration } from './resource-utils'
import { parseGoTimeString } from '../../utils/parse-go-time'

// ============================================================================
// CNPG CLUSTER UTILITIES
// ============================================================================

export const CNPG_GROUP = 'postgresql.cnpg.io'

/**
 * Exact API-group match for a resource's `apiVersion` ("group/version").
 *
 * Used for the `clusters` and `backups` plurals, where several operators ship
 * the same kind and a wrong guess fabricates a status. A substring test is not
 * a group guard — `extension.postgresql.cnpg.io/v1` contains `postgresql.cnpg.io`
 * — and these are exactly the plurals where that would misfire.
 */
export function isApiGroup(apiVersion: unknown, group: string): boolean {
  if (typeof apiVersion !== 'string') return false
  const slash = apiVersion.lastIndexOf('/')
  // A core-group resource ("v1") has no slash and belongs to no CRD group.
  return slash > 0 && apiVersion.slice(0, slash) === group
}

// Phase strings are full English sentences copied verbatim from CNPG's
// api/v1/cluster_types.go — match on equality, never substring. Verified
// against CNPG 1.27. Radar's Go issue detection mirrors these buckets in
// internal/issues/source_cnpg.go; the two must not drift, or a cluster gets
// a red badge and no issue (or the reverse).

/** Cluster is reconciled and serving. */
export const CNPG_CLUSTER_PHASES_HEALTHY = ['Cluster in healthy state'] as const

/** Operator is mid-operation. Expected to resolve on its own. */
export const CNPG_CLUSTER_PHASES_TRANSIENT = [
  'Setting up primary',
  'Creating a new replica',
  'Switchover in progress',
  'Upgrading cluster',
  'Upgrading Postgres major version',
  'Online upgrade in progress',
  'Applying configuration',
  'Primary instance is being restarted in-place',
  'Primary instance is being restarted without a switchover',
  'Promoting to primary cluster',
  'Waiting for the instances to become active',
  // Upstream sets this when an upgrade is postponed by the OPERATOR's own
  // rollout-delay config and requeued — nothing is waiting on a human, so it
  // does not belong with 'Waiting for user action'.
  'Cluster upgrade delayed',
] as const

/** Losing the primary — degraded now, self-resolving only if a replica can take over. */
export const CNPG_CLUSTER_PHASES_FAILING = ['Failing over'] as const

/** Reconciliation has stopped. These need a human; none of them self-heal. */
export const CNPG_CLUSTER_PHASES_TERMINAL = [
  'Cluster is unrecoverable and needs manual intervention',
  // Not in 1.27 or 1.28; added upstream after 1.28 (PhaseDefinitionInvalid).
  // Listed early because matching is by equality — a phase string the running
  // operator never emits is inert, so carrying it costs nothing and stops a
  // future minor from landing in the unknown bucket.
  'Invalid cluster definition',
  'Unable to create required cluster objects',
  'Cluster has incomplete or invalid image catalog',
  'Cluster cannot proceed to reconciliation due to an unknown plugin being required',
  'Cluster cannot proceed to reconciliation due to an error while interacting with plugins',
  'Cluster cannot execute instance online upgrade due to missing architecture binary',
] as const

/**
 * Stalled waiting on a human. Under `primaryUpdateStrategy: supervised` this is
 * the documented, expected resting state, so it is styled as attention rather
 * than failure and the Go side suppresses the issue entirely.
 */
export const CNPG_CLUSTER_PHASES_ATTENTION = [
  'Waiting for user action',
] as const

/**
 * Short display states for the table badge.
 *
 * CNPG's phases are prose, not tokens — "Cluster is unrecoverable and needs
 * manual intervention" is banner copy. At 315px it fits no defensible column,
 * so this is about badge semantics rather than width: a badge carries a state,
 * the drawer and the `title` carry the operator's exact words.
 *
 * Radar's own labels (Healthy, WAL Archiving Failing, …) are not in here —
 * those are already states and are produced directly by getCNPGClusterStatus.
 *
 * Both in-place and without-a-switchover restarts collapse to "Restarting
 * Primary". The distinction is about the operator's mechanism, not about what a
 * scanning operator needs; the exact phase remains in the drawer. Splitting
 * them honestly would need ~140px and a wider column than the rest earns.
 */
const CNPG_PHASE_DISPLAY_STATE: Record<string, string> = {
  // Terminal
  'Cluster is unrecoverable and needs manual intervention': 'Unrecoverable',
  'Invalid cluster definition': 'Invalid Spec',
  'Unable to create required cluster objects': 'Setup Failed',
  'Cluster has incomplete or invalid image catalog': 'Image Catalog Error',
  'Cluster cannot proceed to reconciliation due to an unknown plugin being required': 'Unknown Plugin',
  'Cluster cannot proceed to reconciliation due to an error while interacting with plugins': 'Plugin Error',
  'Cluster cannot execute instance online upgrade due to missing architecture binary': 'Arch Binary Missing',
  // Transient
  'Setting up primary': 'Setting Up Primary',
  'Creating a new replica': 'Creating Replica',
  'Switchover in progress': 'Switchover',
  'Upgrading cluster': 'Upgrading',
  'Upgrading Postgres major version': 'Major Upgrade',
  'Online upgrade in progress': 'Online Upgrade',
  'Applying configuration': 'Configuring',
  'Primary instance is being restarted in-place': 'Restarting Primary',
  'Primary instance is being restarted without a switchover': 'Restarting Primary',
  'Promoting to primary cluster': 'Promoting',
  'Waiting for the instances to become active': 'Starting Instances',
  'Cluster upgrade delayed': 'Upgrade Delayed',
  // Attention
  'Waiting for user action': 'Needs Action',
}

/**
 * The short state for a phase, or the phase itself when we have no mapping.
 *
 * An unrecognized phase from a newer CNPG minor passes through verbatim —
 * inventing a state for a string we have never seen would be worse than showing
 * the operator's words, and that is the case badge truncation exists for.
 */
export function getCNPGClusterDisplayState(phase: string): string {
  return CNPG_PHASE_DISPLAY_STATE[phase] || phase
}

export type CNPGPhaseBucket = 'healthy' | 'transient' | 'failing' | 'terminal' | 'attention' | 'unknown'

export function classifyCNPGClusterPhase(phase: string): CNPGPhaseBucket {
  if (!phase) return 'unknown'
  if ((CNPG_CLUSTER_PHASES_HEALTHY as readonly string[]).includes(phase)) return 'healthy'
  if ((CNPG_CLUSTER_PHASES_TERMINAL as readonly string[]).includes(phase)) return 'terminal'
  if ((CNPG_CLUSTER_PHASES_FAILING as readonly string[]).includes(phase)) return 'failing'
  if ((CNPG_CLUSTER_PHASES_TRANSIENT as readonly string[]).includes(phase)) return 'transient'
  if ((CNPG_CLUSTER_PHASES_ATTENTION as readonly string[]).includes(phase)) return 'attention'
  return 'unknown'
}

export function getCNPGClusterStatus(resource: any): StatusBadge {
  const status = resource.status || {}
  const phase = status.phase || ''
  const instances = resource.spec?.instances ?? 0
  // Readiness PRESENCE matters. A cluster whose status has no readyInstances yet
  // (freshly created, operator hasn't written status) is unknown, not zero —
  // defaulting to 0 fabricated a red "Not Ready" badge for a cluster the issue
  // detector correctly said nothing about.
  const readyKnown = typeof status.readyInstances === 'number'
  const readyInstances: number = readyKnown ? status.readyInstances : 0
  const countsKnown = instances > 0 && readyKnown
  const bucket = classifyCNPGClusterPhase(phase)

  // Ordered by descending severity, and mirrored by source_cnpg.go. Terminal
  // phases outrank instance counts: an unrecoverable cluster whose pods happen
  // to still be Ready is still unrecoverable, and reading the counts first is
  // what used to render it neutral.
  if (bucket === 'terminal') {
    return { text: getCNPGClusterDisplayState(phase), color: healthColors.unhealthy, level: 'unhealthy' }
  }
  // Zero ready instances is a hard down that no phase excuses — including a
  // failover, where the phase alone would otherwise downgrade a total outage to
  // orange.
  if (countsKnown && readyInstances === 0) {
    return { text: 'Not Ready', color: healthColors.unhealthy, level: 'unhealthy' }
  }
  if (bucket === 'failing') {
    return { text: 'Failing Over', color: healthColors.alert, level: 'alert' }
  }
  // Data-durability failures don't move any instance count, so a cluster with a
  // broken recovery point looks perfectly healthy on availability alone — which
  // is precisely how it stays invisible until a restore. The detail drawer has
  // always shown this; the list badge must too, or the headline signal of this
  // integration reads green.
  if (getCNPGWALArchivingFailure(resource)) {
    return { text: 'WAL Archiving Failing', color: healthColors.alert, level: 'alert' }
  }
  // A transient phase explains partial readiness better than a bare "Degraded",
  // so it is checked BEFORE the shortfall — mid-operation the cluster is
  // legitimately below its desired count.
  if (bucket === 'transient' || bucket === 'attention') {
    return { text: getCNPGClusterDisplayState(phase), color: healthColors.degraded, level: 'degraded' }
  }
  // The shortfall must be checked BEFORE the healthy phase, not after. CNPG
  // reports "Cluster in healthy state" the moment reconciliation is settled,
  // which can be true while instances are still missing — returning Healthy
  // there paints a green badge on a cluster the Go detector flags as
  // CNPGClusterDegraded. Mirrors source_cnpg.go's !phaseExplained gate.
  if (countsKnown && readyInstances < instances) {
    return { text: 'Degraded', color: healthColors.degraded, level: 'degraded' }
  }
  if (getCNPGLastBackupFailure(resource)) {
    return { text: 'Backup Failed', color: healthColors.degraded, level: 'degraded' }
  }
  if (bucket === 'healthy') {
    return { text: 'Healthy', color: healthColors.healthy, level: 'healthy' }
  }

  const conditions = status.conditions || []
  const readyCond = conditions.find((c: any) => c.type === 'Ready')

  // Unrecognized phase — surface the operator's own words rather than inventing
  // a health level. A new CNPG minor adding a phase lands here, not in "Healthy".
  //
  // This must outrank the Ready condition below. Ready=True says the instances
  // are serving; it does not say the cluster is fine, and CNPG sets it during
  // phases we have never seen. Reading it first turned an unknown phase into a
  // green "Ready" badge — asserting health from a signal that doesn't establish
  // it, and defeating the pass-through this branch exists for. Ready=False
  // still colors the phase red: showing the operator's words in red loses
  // nothing and keeps a real failure signal.
  if (phase) {
    if (readyCond?.status === 'False') {
      return { text: phase, color: healthColors.unhealthy, level: 'unhealthy' }
    }
    return { text: phase, color: healthColors.unknown, level: 'unknown' }
  }

  // No phase at all — conditions are the only signal left.
  if (readyCond?.status === 'True') {
    return { text: 'Ready', color: healthColors.healthy, level: 'healthy' }
  }
  if (readyCond?.status === 'False') {
    return { text: readyCond.reason || 'Not Ready', color: healthColors.unhealthy, level: 'unhealthy' }
  }

  return { text: 'Unknown', color: healthColors.unknown, level: 'unknown' }
}

// Absence is not zero — same rule the badge follows via readyKnown. Between
// creation and the operator's first status write there is no readyInstances,
// and defaulting it to 0 rendered "0/3" next to a badge that (correctly) did
// not say degraded: the cell claimed a total outage the badge denied.
const countOrDash = (n: unknown): string => (typeof n === 'number' ? String(n) : '-')

export function getCNPGClusterInstances(resource: any): string {
  return `${countOrDash(resource.status?.readyInstances)}/${countOrDash(resource.spec?.instances)}`
}

export function getCNPGClusterPrimary(resource: any): string {
  return resource.status?.currentPrimary || resource.status?.targetPrimary || '-'
}

export function getCNPGClusterPhase(resource: any): string {
  return resource.status?.phase || '-'
}

export function getCNPGClusterImage(resource: any): string {
  return resource.spec?.imageName || '-'
}

export function getCNPGClusterImageTag(resource: any): string {
  const image = resource.spec?.imageName || ''
  if (!image) return '-'
  const parts = image.split(':')
  if (parts.length > 1) return parts[parts.length - 1]
  return image.split('/').pop() || '-'
}

export function getCNPGClusterStorage(resource: any): string {
  return resource.spec?.storage?.size || '-'
}

export function getCNPGClusterStorageClass(resource: any): string {
  return resource.spec?.storage?.storageClass || '-'
}

export function getCNPGClusterWALStorage(resource: any): { size?: string; storageClass?: string } | null {
  const wal = resource.spec?.walStorage
  if (!wal) return null
  return { size: wal.size, storageClass: wal.storageClass }
}

export function getCNPGClusterBootstrapMethod(resource: any): string {
  const bootstrap = resource.spec?.bootstrap
  if (!bootstrap) return '-'
  if (bootstrap.initdb) return 'initdb'
  if (bootstrap.recovery) return 'recovery'
  if (bootstrap.pg_basebackup) return 'pg_basebackup'
  return '-'
}

export function getCNPGClusterUpdateStrategy(resource: any): string {
  return resource.spec?.primaryUpdateStrategy || 'unsupervised'
}

export const CNPG_BARMAN_PLUGIN_NAME = 'barman-cloud.cloudnative-pg.io'

export interface CNPGBarmanPlugin {
  name: string
  /** ObjectStore CR (barmancloud.cnpg.io/v1) holding the real destination + recovery window. */
  barmanObjectName?: string
  /** Resolved server key inside ObjectStore.status.serverRecoveryWindow. */
  serverName?: string
  isWALArchiver: boolean
}

/**
 * Detects the barman-cloud CNPG-I plugin. In-tree `spec.backup.barmanObjectStore`
 * is deprecated as of CNPG 1.26 and the migration moves config into an
 * `ObjectStore` CR in a different API group — at which point CNPG stops setting
 * status.lastSuccessfulBackup / firstRecoverabilityPoint entirely.
 */
export function getCNPGClusterBarmanPlugin(resource: any): CNPGBarmanPlugin | null {
  const plugins = resource.spec?.plugins
  if (!Array.isArray(plugins)) return null
  const plugin = plugins.find(
    (p: any) => p?.name === CNPG_BARMAN_PLUGIN_NAME && p?.enabled !== false
  )
  if (!plugin) return null
  return {
    name: plugin.name,
    barmanObjectName: plugin.parameters?.barmanObjectName,
    // ObjectStore keys the recovery window by the plugin's serverName when set,
    // otherwise by the cluster name.
    serverName: plugin.parameters?.serverName || resource.metadata?.name,
    isWALArchiver: plugin.isWALArchiver === true,
  }
}

export function getCNPGClusterBackupConfig(resource: any): {
  configured: boolean
  destinationPath?: string
  retentionPolicy?: string
  lastSuccessfulBackup?: string
  firstRecoverabilityPoint?: string
  /** Non-null when backup is managed by the barman-cloud plugin. */
  plugin: CNPGBarmanPlugin | null
  /**
   * True when the status RPO fields are deprecated-and-unset by design (plugin
   * mode) rather than genuinely absent. Distinguishes "tracked elsewhere" from
   * "no backups configured" — the ambiguity that made this section render blank.
   */
  rpoTrackedOnObjectStore: boolean
} {
  const backup = resource.spec?.backup
  const barman = backup?.barmanObjectStore
  const plugin = getCNPGClusterBarmanPlugin(resource)
  return {
    configured: !!barman || !!plugin,
    destinationPath: barman?.destinationPath,
    retentionPolicy: backup?.retentionPolicy,
    lastSuccessfulBackup: resource.status?.lastSuccessfulBackup,
    firstRecoverabilityPoint: resource.status?.firstRecoverabilityPoint,
    plugin,
    rpoTrackedOnObjectStore: !!plugin && !resource.status?.lastSuccessfulBackup,
  }
}

export function getCNPGClusterMonitoring(resource: any): {
  podMonitorEnabled: boolean
  customQueriesConfigMap?: string[]
} {
  const monitoring = resource.spec?.monitoring
  return {
    podMonitorEnabled: monitoring?.enablePodMonitor === true,
    customQueriesConfigMap: monitoring?.customQueriesConfigMap?.map((ref: any) =>
      typeof ref === 'string' ? ref : ref.name
    ),
  }
}

export function getCNPGClusterIsReplica(resource: any): boolean {
  const replica = resource.spec?.replica
  if (!replica) return false
  // Presence of the block is not replica-hood: promoting a replica leaves the
  // block in place and flips `enabled` to false.
  if (typeof replica.enabled === 'boolean') return replica.enabled
  // Distributed topology carries no `enabled`. CNPG decides the role by
  // comparing `self` against `primary`, defaulting `self` to the cluster name.
  if (replica.primary) {
    const self = replica.self || resource.metadata?.name
    return !!self && self !== replica.primary
  }
  return false
}

export function getCNPGClusterReplicaSource(resource: any): string {
  const replica = resource.spec?.replica
  if (!replica) return '-'
  return replica.source || replica.primary || '-'
}

export function getCNPGClusterInstanceNames(resource: any): string[] {
  return resource.status?.instanceNames || []
}

export function getCNPGClusterPostgresParams(resource: any): Record<string, string> {
  return resource.spec?.postgresql?.parameters || {}
}

/** Per-instance replication state from status.instancesReportedState */
export interface CNPGInstanceReportedState {
  podName: string
  isPrimary: boolean
  timelineID?: number
  ip?: string
}

export function getCNPGClusterInstancesReportedState(resource: any): CNPGInstanceReportedState[] {
  const reported = resource.status?.instancesReportedState
  if (!reported || typeof reported !== 'object') return []
  return Object.entries(reported).map(([podName, state]: [string, any]) => ({
    podName,
    isPrimary: state?.isPrimary === true,
    // CNPG serializes this as timeLineID (capital L).
    timelineID: state?.timeLineID,
    ip: state?.ip,
  }))
}

/** Certificate expirations from status.certificates.expirations */
export interface CNPGCertificateExpiry {
  secretName: string
  expiryDate: string
  daysUntilExpiry: number
}

export function getCNPGClusterCertificateExpirations(resource: any): CNPGCertificateExpiry[] {
  const expirations = resource.status?.certificates?.expirations
  if (!expirations || typeof expirations !== 'object') return []
  const now = new Date()
  return Object.entries(expirations).map(([secretName, expiryDate]: [string, any]) => {
    const expiry = parseGoTimeString(String(expiryDate))
    const daysUntilExpiry = isNaN(expiry.getTime())
      ? -1  // treat unparseable dates as expired/critical
      : Math.floor((expiry.getTime() - now.getTime()) / (1000 * 60 * 60 * 24))
    return { secretName, expiryDate: String(expiryDate), daysUntilExpiry }
  })
}

// ============================================================================
// CNPG CLUSTER CONDITIONS — WAL ARCHIVING / BACKUP HEALTH
// ============================================================================

export interface CNPGCondition {
  type: string
  status: string
  reason?: string
  message?: string
  lastTransitionTime?: string
}

export function getCNPGClusterCondition(resource: any, type: string): CNPGCondition | null {
  const conditions = resource.status?.conditions
  if (!Array.isArray(conditions)) return null
  const cond = conditions.find((c: any) => c?.type === type)
  if (!cond) return null
  return {
    type: cond.type,
    status: String(cond.status ?? ''),
    reason: cond.reason,
    message: cond.message,
    lastTransitionTime: cond.lastTransitionTime,
  }
}

/**
 * WAL archiving failure — the classic silent CNPG incident. The cluster keeps
 * serving traffic normally while the recovery point stops advancing.
 *
 * The condition proves the LAST ARCHIVE ATTEMPT failed, not an exact RPO, so
 * callers must not turn `since` into a data-loss window.
 */
export function getCNPGWALArchivingFailure(resource: any): CNPGCondition | null {
  const cond = getCNPGClusterCondition(resource, 'ContinuousArchiving')
  if (!cond || cond.status !== 'False') return null
  return cond
}

/**
 * Last-backup failure. CNPG also sets LastBackupSucceeded=False with reason
 * `BackupStarted` while a backup is merely in flight — treating that as a
 * failure would light the banner on every backup run, so only an explicit
 * LastBackupFailed reason counts.
 */
export function getCNPGLastBackupFailure(resource: any): CNPGCondition | null {
  const cond = getCNPGClusterCondition(resource, 'LastBackupSucceeded')
  if (!cond || cond.status !== 'False') return null
  if (cond.reason === 'BackupStarted') return null
  return cond
}

// ============================================================================
// CNPG BACKUP UTILITIES
// ============================================================================

// Backup phases are lowercase tokens from CNPG's api/v1/backup_types.go,
// verified against CNPG 1.27. Unlike cluster phases these are stable enum
// tokens, but they are still matched exactly — an unrecognized phase must
// surface as unknown rather than be swallowed by a prefix match.
export const CNPG_BACKUP_PHASES_TRANSIENT = ['pending', 'started', 'running', 'finalizing'] as const

/**
 * WAL archiving is broken upstream of this Backup, so the recovery window for
 * the whole cluster is affected — not just this one object. Kept separate from
 * a plain `failed` so callers can escalate it to the cluster level.
 */
export const CNPG_BACKUP_PHASE_WAL_ARCHIVING_FAILING = 'walArchivingFailing'

export type CNPGBackupPhaseBucket = 'healthy' | 'transient' | 'failed' | 'walArchivingFailing' | 'unknown'

export function classifyCNPGBackupPhase(phase: string): CNPGBackupPhaseBucket {
  if (!phase) return 'unknown'
  if (phase === CNPG_BACKUP_PHASE_WAL_ARCHIVING_FAILING) return 'walArchivingFailing'
  if (phase === 'completed') return 'healthy'
  if (phase === 'failed') return 'failed'
  if ((CNPG_BACKUP_PHASES_TRANSIENT as readonly string[]).includes(phase)) return 'transient'
  return 'unknown'
}

export function getCNPGBackupStatus(resource: any): StatusBadge {
  const phase = resource.status?.phase || ''

  switch (classifyCNPGBackupPhase(phase)) {
    case 'healthy':
      return { text: 'Completed', color: healthColors.healthy, level: 'healthy' }
    case 'transient':
      return { text: phase.charAt(0).toUpperCase() + phase.slice(1), color: healthColors.degraded, level: 'degraded' }
    case 'failed':
      return { text: 'Failed', color: healthColors.unhealthy, level: 'unhealthy' }
    case 'walArchivingFailing':
      return { text: 'WAL Archiving Failing', color: healthColors.unhealthy, level: 'unhealthy' }
    default:
      if (phase) return { text: phase, color: healthColors.unknown, level: 'unknown' }
      return { text: 'Unknown', color: healthColors.unknown, level: 'unknown' }
  }
}

export function getCNPGBackupCluster(resource: any): string {
  return resource.spec?.cluster?.name || '-'
}

export function getCNPGBackupMethod(resource: any): string {
  return resource.status?.method || resource.spec?.method || '-'
}

export function getCNPGBackupPhase(resource: any): string {
  return resource.status?.phase || '-'
}

export function getCNPGBackupDuration(resource: any): string {
  const started = resource.status?.startedAt
  const stopped = resource.status?.stoppedAt
  if (!started) return '-'
  const startTime = new Date(started).getTime()
  const endTime = stopped ? new Date(stopped).getTime() : Date.now()
  return formatDuration(endTime - startTime, true)
}

export function getCNPGBackupStartedAt(resource: any): string {
  return resource.status?.startedAt ? formatAge(resource.status.startedAt) : '-'
}

export function getCNPGBackupStoppedAt(resource: any): string {
  return resource.status?.stoppedAt ? formatAge(resource.status.stoppedAt) : '-'
}

export function getCNPGBackupName(resource: any): string {
  return resource.status?.backupName || '-'
}

export function getCNPGBackupDestinationPath(resource: any): string {
  return resource.status?.destinationPath || '-'
}

export function getCNPGBackupServerName(resource: any): string {
  return resource.status?.serverName || '-'
}

export function getCNPGBackupError(resource: any): string {
  return resource.status?.error || ''
}

export function getCNPGBackupTarget(resource: any): string {
  return resource.spec?.target || '-'
}

// ============================================================================
// CNPG SCHEDULED BACKUP UTILITIES
// ============================================================================

export function getCNPGScheduledBackupStatus(resource: any): StatusBadge {
  const isSuspended = resource.spec?.suspend === true

  if (isSuspended) {
    return { text: 'Suspended', color: healthColors.neutral, level: 'neutral' }
  }

  // If we have a last schedule time, it's active
  if (resource.status?.lastScheduleTime) {
    return { text: 'Active', color: healthColors.healthy, level: 'healthy' }
  }

  // Check if immediate flag is set and no schedule has run yet
  if (resource.spec?.immediate) {
    return { text: 'Immediate', color: healthColors.healthy, level: 'healthy' }
  }

  return { text: 'Scheduled', color: healthColors.healthy, level: 'healthy' }
}

export function getCNPGScheduledBackupCluster(resource: any): string {
  return resource.spec?.cluster?.name || '-'
}

export function getCNPGScheduleCron(resource: any): string {
  return resource.spec?.schedule || '-'
}

export function getCNPGScheduledBackupMethod(resource: any): string {
  return resource.spec?.method || 'barmanObjectStore'
}

export function getCNPGScheduledBackupLastSchedule(resource: any): string {
  return resource.status?.lastScheduleTime ? formatAge(resource.status.lastScheduleTime) : '-'
}

export function getCNPGScheduledBackupNextSchedule(resource: any): string {
  const next = resource.status?.nextScheduleTime
  if (!next) return '-'
  const nextDate = new Date(next)
  const now = new Date()
  const diffMs = nextDate.getTime() - now.getTime()
  if (diffMs <= 0) return 'overdue'
  return `in ${formatDuration(diffMs)}`
}

export function getCNPGScheduledBackupIsSuspended(resource: any): boolean {
  return resource.spec?.suspend === true
}

export function getCNPGScheduledBackupIsImmediate(resource: any): boolean {
  return resource.spec?.immediate === true
}

export function getCNPGScheduledBackupOwnerRef(resource: any): string {
  return resource.spec?.backupOwnerReference || 'none'
}

// ============================================================================
// CNPG POOLER UTILITIES
// ============================================================================

/**
 * Pooler.status.instances is "the number of pods trying to be scheduled" — NOT
 * ready pods. A Pooler whose PgBouncer pods are all Pending still reports
 * status.instances == spec.instances, so reading it as readiness renders a
 * broken Pooler green. Readiness lives on the generated Deployment (same name,
 * same namespace), which the Pooler CR alone cannot see — so the positive state
 * is labelled "Scheduled", not "Ready".
 */
export function getCNPGPoolerStatus(resource: any): StatusBadge {
  const desired = resource.spec?.instances ?? 0
  // Presence, not value — the same distinction getCNPGClusterStatus makes. A
  // Pooler the controller has not reconciled yet has no status.instances at
  // all, and reading that as 0 rendered a red "Not Scheduled" on a Pooler that
  // may be perfectly healthy: a fabricated failure, not merely a wrong count.
  const scheduledKnown = typeof resource.status?.instances === 'number'
  const scheduled = scheduledKnown ? resource.status.instances : 0

  if (!scheduledKnown) {
    return { text: 'Unknown', color: healthColors.unknown, level: 'unknown' }
  }

  if (desired > 0 && scheduled < desired) {
    if (scheduled === 0) {
      return { text: 'Not Scheduled', color: healthColors.unhealthy, level: 'unhealthy' }
    }
    return { text: 'Degraded', color: healthColors.degraded, level: 'degraded' }
  }

  // Faults outrank intent: a Pooler that is both paused and unscheduled has a
  // problem worth fixing before the pause is worth mentioning.
  if (isCNPGPoolerPaused(resource)) {
    return { text: 'Paused', color: healthColors.degraded, level: 'degraded' }
  }

  if (desired > 0 && scheduled >= desired) {
    return { text: 'Scheduled', color: healthColors.healthy, level: 'healthy' }
  }

  return { text: 'Unknown', color: healthColors.unknown, level: 'unknown' }
}

/**
 * PgBouncer holds client connections instead of serving them while paused, and
 * every pod stays scheduled and Ready throughout - the one Pooler state that
 * pod counts cannot express. CRD-defaulted to false, so only an explicit true
 * pauses.
 */
export function isCNPGPoolerPaused(resource: any): boolean {
  return resource.spec?.pgbouncer?.paused === true
}

/** Name of the Deployment CNPG generates for this Pooler — where real readiness lives. */
export function getCNPGPoolerDeploymentName(resource: any): string {
  return resource.metadata?.name || ''
}

export function getCNPGPoolerCluster(resource: any): string {
  return resource.spec?.cluster?.name || '-'
}

export function getCNPGPoolerType(resource: any): string {
  return resource.spec?.type || '-'
}

export function getCNPGPoolerMode(resource: any): string {
  return resource.spec?.pgbouncer?.poolMode || 'session'
}

export function getCNPGPoolerInstances(resource: any): string {
  return `${countOrDash(resource.status?.instances)}/${countOrDash(resource.spec?.instances)}`
}

export function getCNPGPoolerParameters(resource: any): Record<string, string> {
  return resource.spec?.pgbouncer?.parameters || {}
}

export function getCNPGPoolerAuthQuery(resource: any): string | undefined {
  return resource.spec?.pgbouncer?.authQuery
}

export function getCNPGPoolerAuthQuerySecret(resource: any): { name: string } | undefined {
  const secret = resource.spec?.pgbouncer?.authQuerySecret
  if (!secret?.name) return undefined
  return { name: secret.name }
}

/**
 * Volume health for a CNPG cluster, read from the arrays the operator publishes
 * on status. Radar previously showed only the REQUESTED size and storage class,
 * which are spec-side intentions — a cluster whose volumes had failed rendered
 * a cheerful "20Gi" with nothing to suggest otherwise.
 *
 * `unusablePVC` is the sharp one. Upstream defines it as a volume that cannot be
 * used because a paired volume is missing, which is why a database instance can
 * be permanently unable to start while every other field looks ordinary.
 *
 * Returns null when the operator has published nothing, so the section can stay
 * silent rather than claim zero problems it never checked for.
 */
export interface CNPGVolumeHealth {
  total?: number
  healthy: string[]
  unusable: string[]
  dangling: string[]
  resizing: string[]
  initializing: string[]
}

export function getCNPGVolumeHealth(resource: any): CNPGVolumeHealth | null {
  const s = resource?.status
  if (!s) return null
  const list = (v: any): string[] => (Array.isArray(v) ? v.filter((x: any) => typeof x === 'string') : [])
  const health: CNPGVolumeHealth = {
    total: typeof s.pvcCount === 'number' ? s.pvcCount : undefined,
    healthy: list(s.healthyPVC),
    unusable: list(s.unusablePVC),
    dangling: list(s.danglingPVC),
    resizing: list(s.resizingPVC),
    initializing: list(s.initializingPVC),
  }
  const reportedAnything =
    health.total !== undefined ||
    health.healthy.length > 0 ||
    health.unusable.length > 0 ||
    health.dangling.length > 0 ||
    health.resizing.length > 0 ||
    health.initializing.length > 0
  return reportedAnything ? health : null
}
