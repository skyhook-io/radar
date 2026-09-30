import { CNPG_BARMAN_PLUGIN_NAME, getCNPGClusterBarmanPlugin, getCNPGObjectStoreRecoveryWindows, isApiGroup, type HealthLevel } from '@skyhook-io/k8s-ui'
import type { CNPGActionCapability, CNPGRuntimeResponse } from '../../../api/cnpg'
import type { CNPGRecoveryResponse, CNPGRecoveryPod } from '../../../api/cnpg-recovery'

export const RESTORE_VALIDATION_ANNOTATION = 'radar.skyhook.io/restore-validation'
const HEALTHY_PHASE = 'Cluster in healthy state'

/**
 * Where a restore reads from. Plugin backups are restored through the
 * ObjectStore with the backup's ID pinned: CloudNativePG restores a
 * `bootstrap.recovery.backup` reference only from in-tree or snapshot backups.
 */
export type RestoreSource =
  | { kind: 'objectStore'; objectStore: string; serverName: string; backupID?: string; backupName?: string; backupEnd?: string }
  | { kind: 'inTree'; barmanObjectStore: Record<string, unknown>; serverName: string }
  | { kind: 'backup'; backup: string; backupEnd?: string }

export type RestoreTarget = { kind: 'latest' } | { kind: 'time'; iso: string } | { kind: 'backupEnd' }

function isPluginBackup(b: any): boolean {
  return b?.spec?.method === 'plugin' || b?.status?.method === 'plugin'
}

function backupEnd(b: any): string | undefined {
  return b?.status?.stoppedAt || b?.status?.startedAt
}

/** The source a Backup restores from, given the cluster that took it (if it still exists). */
export function restoreSourceForBackup(backup: any, sourceCluster: any | null): RestoreSource | null {
  if (backup?.status?.phase !== 'completed') return null
  const name = backup.metadata?.name
  if (!isPluginBackup(backup)) return { kind: 'backup', backup: name, backupEnd: backupEnd(backup) }
  const params = backup.spec?.pluginConfiguration?.parameters ?? {}
  const plugin = (sourceCluster?.spec?.plugins ?? []).find((p: any) => p?.name === CNPG_BARMAN_PLUGIN_NAME)
  const objectStore = params.barmanObjectName || plugin?.parameters?.barmanObjectName
  const serverName = params.serverName || plugin?.parameters?.serverName || backup.spec?.cluster?.name
  const backupID = backup.status?.backupId
  if (!objectStore || !serverName || !backupID) return null
  return { kind: 'objectStore', objectStore, serverName, backupID, backupName: name, backupEnd: backupEnd(backup) }
}

/** Where a Cluster's backups can be restored from, in order of preference. */
export function restoreSourcesFor(cluster: any, backups: any[]): RestoreSource[] {
  const name = cluster?.metadata?.name
  const out: RestoreSource[] = []
  const plugin = (cluster?.spec?.plugins ?? []).find((p: any) => p?.name === CNPG_BARMAN_PLUGIN_NAME && p?.enabled !== false)
  if (plugin?.parameters?.barmanObjectName) {
    out.push({ kind: 'objectStore', objectStore: plugin.parameters.barmanObjectName, serverName: plugin.parameters.serverName || name })
  }
  const inTree = cluster?.spec?.backup?.barmanObjectStore
  if (inTree) out.push({ kind: 'inTree', barmanObjectStore: inTree, serverName: inTree.serverName || name })
  const completed = backups
    .filter((b) => b?.spec?.cluster?.name === name && b?.metadata?.namespace === cluster?.metadata?.namespace && isApiGroup(b?.apiVersion ?? 'postgresql.cnpg.io/v1', 'postgresql.cnpg.io'))
    .sort((a, b) => Date.parse(backupEnd(b) ?? '') - Date.parse(backupEnd(a) ?? ''))
  for (const b of completed) {
    const s = restoreSourceForBackup(b, cluster)
    if (s) out.push(s)
  }
  return out
}

/** One ObjectStore source per server its status reports, newest evidence first. */
export function restoreSourcesForStore(store: any): RestoreSource[] {
  const name = store?.metadata?.name
  return getCNPGObjectStoreRecoveryWindows(store)
    .sort((a, b) => Date.parse(b.lastSuccessfulBackupTime ?? '') - Date.parse(a.lastSuccessfulBackupTime ?? ''))
    .map((w) => ({ kind: 'objectStore' as const, objectStore: name, serverName: w.server }))
}

/** The live Cluster that archives into this source, when there is one. */
export function sourceClusterFor(source: RestoreSource, clusters: any[], namespace: string): any | null {
  if (source.kind === 'backup') return null
  return (
    clusters.find((c) => {
      if (c?.metadata?.namespace !== namespace || c?.spec?.bootstrap?.recovery) return false
      if (source.kind === 'objectStore') {
        const p = (c.spec?.plugins ?? []).find((x: any) => x?.name === CNPG_BARMAN_PLUGIN_NAME)
        return p?.parameters?.barmanObjectName === source.objectStore && (p?.parameters?.serverName || c.metadata?.name) === source.serverName
      }
      return (c.spec?.backup?.barmanObjectStore?.serverName || c.metadata?.name) === source.serverName
    }) ?? null
  )
}

export function describeSource(s: RestoreSource): string {
  switch (s.kind) {
    case 'objectStore':
      return s.backupName ? `Backup ${s.backupName} (ID ${s.backupID}) in ObjectStore ${s.objectStore}` : `ObjectStore ${s.objectStore} · server ${s.serverName}`
    case 'inTree':
      return `Barman object store (in-tree) · server ${s.serverName}`
    default:
      return `Backup ${s.backup}`
  }
}

/** A source pinned to one base backup can also stop at that backup's end. */
export function sourcePinsBackup(s: RestoreSource | undefined): boolean {
  return s?.kind === 'objectStore' && !!s.backupID
}

export interface EvidencePoint {
  at: string
  source: string
}

/**
 * What Radar can show about how far back and how far forward a restore can
 * reach. Each point names where it came from; a missing point is unknown,
 * never "none".
 */
export interface RecoveryEvidence {
  firstPoint?: EvidencePoint
  lastBackup?: EvidencePoint
  lastArchived?: EvidencePoint & { wal?: string }
  lastArchiveFailure?: EvidencePoint & { wal?: string }
  archiving: { text: string; tone: HealthLevel; source: string }
  gaps: string[]
}

function newest(points: (EvidencePoint | undefined)[]): EvidencePoint | undefined {
  return points.filter((p): p is EvidencePoint => !!p && Number.isFinite(Date.parse(p.at))).sort((a, b) => Date.parse(b.at) - Date.parse(a.at))[0]
}

export function recoveryEvidenceFor(
  source: RestoreSource | undefined,
  ctx: { sourceCluster: any | null; stores: any[]; backups: any[]; runtime?: CNPGRuntimeResponse; namespace: string },
): RecoveryEvidence {
  const gaps: string[] = []
  const out: RecoveryEvidence = { archiving: { text: 'Not reported', tone: 'unknown', source: 'No source cluster to read' }, gaps }
  if (!source) return out
  const cluster = ctx.sourceCluster
  const clusterName = cluster?.metadata?.name

  if (source.kind === 'objectStore') {
    const store = ctx.stores.find((s) => s?.metadata?.namespace === ctx.namespace && s?.metadata?.name === source.objectStore)
    if (!store) gaps.push(`ObjectStore ${source.objectStore} is not readable, so its recovery window is unknown`)
    const w = store ? getCNPGObjectStoreRecoveryWindows(store).find((x) => x.server === source.serverName) : undefined
    if (store && !w) gaps.push(`ObjectStore ${source.objectStore} reports no recovery window for server ${source.serverName}`)
    const where = `ObjectStore ${source.objectStore} status, server ${source.serverName}`
    if (w?.firstRecoverabilityPoint) out.firstPoint = { at: w.firstRecoverabilityPoint, source: where }
    if (w?.lastSuccessfulBackupTime) out.lastBackup = { at: w.lastSuccessfulBackupTime, source: where }
  } else if (source.kind === 'inTree' && cluster) {
    if (cluster.status?.firstRecoverabilityPoint) out.firstPoint = { at: cluster.status.firstRecoverabilityPoint, source: `Cluster ${clusterName} status` }
    if (cluster.status?.lastSuccessfulBackup) out.lastBackup = { at: cluster.status.lastSuccessfulBackup, source: `Cluster ${clusterName} status` }
  }

  const ownBackups = ctx.backups.filter(
    (b) => b?.metadata?.namespace === ctx.namespace && b?.status?.phase === 'completed' && (clusterName ? b?.spec?.cluster?.name === clusterName : false),
  )
  const newestBackup = newest(ownBackups.map((b) => (backupEnd(b) ? { at: backupEnd(b)!, source: `Backup ${b.metadata?.name}` } : undefined)))
  out.lastBackup = newest([out.lastBackup, newestBackup])

  if (cluster) {
    const cond = (cluster.status?.conditions ?? []).find((c: any) => c?.type === 'ContinuousArchiving')
    if (!cond) out.archiving = { text: 'Not reported', tone: 'unknown', source: `Cluster ${clusterName} has no ContinuousArchiving condition` }
    else if (cond.status === 'True') out.archiving = { text: 'Archiving', tone: 'healthy', source: `ContinuousArchiving condition on ${clusterName}` }
    else if (cond.status === 'False') out.archiving = { text: cond.message ? `Failing · ${cond.message}` : 'Failing', tone: 'unhealthy', source: `ContinuousArchiving condition on ${clusterName}` }
    else out.archiving = { text: 'Unknown', tone: 'unknown', source: `ContinuousArchiving condition on ${clusterName}` }

    const primary = ctx.runtime?.instances.find((i) => i.role === 'primary')
    const arch = primary?.status.state === 'ok' ? primary.status.archiving : undefined
    if (arch?.lastArchivedAt) out.lastArchived = { at: arch.lastArchivedAt, wal: arch.lastArchivedWal, source: `instance manager on ${primary!.pod}` }
    if (arch?.lastFailedAt) out.lastArchiveFailure = { at: arch.lastFailedAt, wal: arch.lastFailedWal, source: `instance manager on ${primary!.pod}` }
    if (!arch) {
      gaps.push(
        !ctx.runtime
          ? 'The last archived WAL time is not loaded'
          : ctx.runtime.permission.proxy === 'denied'
            ? `The last archived WAL time needs ${ctx.runtime.permission.grant ?? 'get pods/proxy'}`
            : !primary
              ? 'No primary is reported, so the last archived WAL time is unknown'
              : primary.status.state !== 'ok'
                ? `The primary’s instance manager could not be read${primary.status.error ? ` (${primary.status.error})` : ''}, so the last archived WAL time is unknown`
                : 'The primary’s instance manager did not report archiving',
      )
    }
  } else if (source.kind !== 'backup') {
    out.archiving = { text: 'Unknown', tone: 'unknown', source: 'No live cluster archives into this source' }
    gaps.push('No live cluster archives into this source, so WAL archived after the last backup is unknown')
  }
  if (!out.firstPoint && source.kind !== 'backup') gaps.push('The first recoverability point is not reported')
  return out
}

/** The newest moment any evidence says the archive reaches. */
export function latestEvidence(e: RecoveryEvidence): EvidencePoint | undefined {
  return newest([e.lastBackup, e.lastArchived])
}

/**
 * Warnings for a point-in-time target. Never a block: the evidence can be
 * stale or partial, and the operator is the authority on what the archive
 * holds.
 */
export function pitrWarnings(targetIso: string, e: RecoveryEvidence, now = Date.now()): string[] {
  const t = Date.parse(targetIso)
  if (!Number.isFinite(t)) return ['The target time is not a valid date.']
  const out: string[] = []
  if (t > now) out.push('The target is in the future; recovery cannot reach it and will fail.')
  if (e.firstPoint && t < Date.parse(e.firstPoint.at)) {
    out.push(`The target is before the first recoverability point (${formatUTC(e.firstPoint.at)}, from ${e.firstPoint.source}); no base backup is old enough and recovery will fail.`)
  }
  const latest = latestEvidence(e)
  if (latest && t > Date.parse(latest.at) && t <= now) {
    out.push(`The target is after the newest evidence Radar has (${formatUTC(latest.at)}, from ${latest.source}). If WAL up to the target was not archived, PostgreSQL stops recovery with an error.`)
  }
  if (!latest && !e.firstPoint) out.push('Radar has no evidence of what this source holds, so the target cannot be checked.')
  return out
}

export function formatUTC(iso: string): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso
  return `${d.toISOString().slice(0, 19).replace('T', ' ')} UTC`
}

export function formatLocal(iso: string): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso
  const tz = Intl.DateTimeFormat().resolvedOptions().timeZone
  return `${d.toLocaleString(undefined, { year: 'numeric', month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit', second: '2-digit' })} ${tz}`
}

/** A datetime-local value read in UTC or the browser's zone, as RFC 3339 without milliseconds. */
export function targetIsoFrom(value: string, zone: 'utc' | 'local'): string | null {
  if (!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}(:\d{2})?$/.test(value)) return null
  const withSeconds = value.length === 16 ? `${value}:00` : value
  const d = zone === 'utc' ? new Date(`${withSeconds}Z`) : new Date(withSeconds)
  if (Number.isNaN(d.getTime())) return null
  return d.toISOString().replace(/\.\d{3}Z$/, 'Z')
}

export interface PreflightFact {
  label: string
  value: string
  path: string
  copied: boolean
}

/** What the manifest copies from the source cluster, to review before creating. */
export function preflightFacts(cluster: any | null): PreflightFact[] {
  const spec = cluster?.spec ?? {}
  const facts: PreflightFact[] = []
  const add = (label: string, path: string, value: string | undefined, fallback: string) =>
    facts.push({ label, path, value: value ?? fallback, copied: value !== undefined })
  add('Instances', 'spec.instances', spec.instances !== undefined ? String(spec.instances) : undefined, '1 (default; no source cluster to copy from)')
  if (spec.imageCatalogRef) {
    const ref = spec.imageCatalogRef
    add('Image', 'spec.imageCatalogRef', `${ref.kind ?? 'ImageCatalog'} ${ref.name}, major ${ref.major}`, '')
  } else {
    add('Image', 'spec.imageName', spec.imageName ?? cluster?.status?.image, 'operator default — set it to the source’s PostgreSQL major')
  }
  add('Data storage', 'spec.storage', spec.storage ? storageText(spec.storage) : undefined, 'not set — set spec.storage.size to at least the source’s data size')
  if (spec.walStorage) add('WAL storage', 'spec.walStorage', storageText(spec.walStorage), '')
  if (Array.isArray(spec.tablespaces) && spec.tablespaces.length > 0) {
    add('Tablespaces', 'spec.tablespaces', spec.tablespaces.map((t: any) => `${t.name} (${storageText(t.storage ?? {})})`).join(', '), '')
  }
  const params = spec.postgresql?.parameters
  if (params && Object.keys(params).length > 0) {
    const keys = Object.keys(params).sort()
    add('PostgreSQL parameters', 'spec.postgresql.parameters', `${keys.length}: ${keys.slice(0, 6).join(', ')}${keys.length > 6 ? ', …' : ''}`, '')
  }
  if (spec.resources && Object.keys(spec.resources).length > 0) add('Resources', 'spec.resources', JSON.stringify(spec.resources), '')
  return facts
}

function storageText(s: any): string {
  const parts = [s.size ?? s.pvcTemplate?.resources?.requests?.storage ?? 'size from pvcTemplate', s.storageClass ? `class ${s.storageClass}` : null]
  return parts.filter(Boolean).join(', ')
}

/**
 * A new Cluster that bootstraps from `source`. It copies what the restored
 * data needs from the source cluster and nothing that would make it write to
 * the source's backup location: no plugins, no backup section.
 */
export function buildRestoreManifest(args: {
  sourceCluster: any | null
  source: RestoreSource
  namespace: string
  newName: string
  target: RestoreTarget
}): Record<string, any> {
  const { sourceCluster, source, namespace, newName, target } = args
  const spec = sourceCluster?.spec ?? {}
  const recovery: Record<string, any> = source.kind === 'backup' ? { backup: { name: source.backup } } : { source: 'origin' }
  const pinned = source.kind === 'objectStore' && source.backupID ? { backupID: source.backupID } : {}
  if (target.kind === 'backupEnd' && 'backupID' in pinned) recovery.recoveryTarget = { ...pinned, targetImmediate: true }
  else if (target.kind === 'time') recovery.recoveryTarget = { ...pinned, targetTime: target.iso }
  else if ('backupID' in pinned) recovery.recoveryTarget = pinned
  const out: Record<string, any> = {
    apiVersion: 'postgresql.cnpg.io/v1',
    kind: 'Cluster',
    metadata: { name: newName, namespace },
    spec: {
      instances: spec.instances ?? 1,
      ...(spec.imageCatalogRef ? { imageCatalogRef: spec.imageCatalogRef } : spec.imageName ? { imageName: spec.imageName } : {}),
      ...(spec.postgresql?.parameters ? { postgresql: { parameters: spec.postgresql.parameters } } : {}),
      ...(spec.resources ? { resources: spec.resources } : {}),
      storage: spec.storage ?? { size: '1Gi' },
      ...(spec.walStorage ? { walStorage: spec.walStorage } : {}),
      ...(spec.tablespaces ? { tablespaces: spec.tablespaces } : {}),
      bootstrap: { recovery },
    },
  }
  if (source.kind === 'objectStore') {
    out.spec.externalClusters = [
      { name: 'origin', plugin: { name: CNPG_BARMAN_PLUGIN_NAME, parameters: { barmanObjectName: source.objectStore, serverName: source.serverName } } },
    ]
  } else if (source.kind === 'inTree') {
    out.spec.externalClusters = [{ name: 'origin', barmanObjectStore: { ...source.barmanObjectStore, serverName: source.serverName } }]
  }
  return out
}

export function restoreManifestHeader(sourceLabel: string, sourceServer: string | undefined, copiedFrom: string | null): string {
  const lines = [
    `# Restores ${sourceLabel} into a new cluster. Review before creating:`,
    copiedFrom
      ? `# - instances, image, storage, WAL storage, tablespaces, PostgreSQL parameters and resources are copied from ${copiedFrom}; edit them here.`
      : '# - no source cluster was found: image and storage are placeholders; set them before creating.',
    '# - the new cluster has no WAL archiving or backups until you configure them.',
    sourceServer
      ? `#   When you do, use a new serverName: archiving under "${sourceServer}" would write into the archive this restores from.`
      : '#   When you do, use a new destination: never the location this restores from.',
    '# - the dry-run checks the manifest, not whether the backups are readable.',
  ]
  return `${lines.join('\n')}\n`
}

// ---------- restore follow-through ----------

export type RestoreState = 'progressing' | 'completed' | 'failed' | 'unobservable'

export interface RestoreObservation {
  state: RestoreState
  title: string
  detail?: string
  tone: HealthLevel
  /** The Pod whose logs explain the current step. */
  logsPod?: { name: string; container?: string }
  recoveryPods: CNPGRecoveryPod[]
}

const FAILING_PHASE = /unrecoverable|unable to|cannot|error|failed/i
const CRASH_REASONS = new Set(['CrashLoopBackOff', 'Error', 'ImagePullBackOff', 'ErrImagePull', 'CreateContainerConfigError', 'CreateContainerError'])

function failingContainer(p: CNPGRecoveryPod) {
  return [...p.initContainers, ...p.containers].find(
    (c) => (c.state === 'waiting' && c.reason && CRASH_REASONS.has(c.reason)) || (c.state === 'terminated' && c.exitCode !== undefined && c.exitCode !== 0),
  )
}

// A running init container is a restartable sidecar (the plugin's), not the
// step in progress; an init container still waiting is.
function activeContainer(p: CNPGRecoveryPod) {
  const init = p.initContainers.find((c) => c.state === 'waiting' || c.state === 'unknown' || (c.state === 'terminated' && (c.exitCode ?? 0) !== 0))
  return init ?? p.containers.find((c) => c.state === 'running') ?? p.containers[0]
}

/**
 * Where a restore stands, from the recovery snapshot alone. Completion is the
 * Cluster reporting a healthy phase with every instance ready; failure is a
 * failed recovery Job or a failing phase. Missing Pod access never reads as
 * progress or failure.
 */
export function observeRestore(snap: CNPGRecoveryResponse): RestoreObservation {
  const recoveryPods = snap.pods.filter((p) => p.kind === 'job')
  const instances = snap.pods.filter((p) => p.kind === 'instance')
  const c = snap.cluster
  const base = { recoveryPods }
  const podsVisible = snap.coverage.pods?.state === 'ok'

  const failedJob = snap.jobs.find((j) => j.failedWith)
  if (failedJob) {
    const pod = recoveryPods.find((p) => p.job === failedJob.name)
    return { ...base, state: 'failed', tone: 'unhealthy', title: `Recovery Job ${failedJob.name} failed`, detail: failedJob.failedWith, logsPod: pod ? { name: pod.name, container: failingContainer(pod)?.name } : undefined }
  }
  if (c.phase && c.phase !== HEALTHY_PHASE && FAILING_PHASE.test(c.phase)) {
    return { ...base, state: 'failed', tone: 'unhealthy', title: c.phase, detail: c.phaseReason || c.ready?.message }
  }
  if (c.phase === HEALTHY_PHASE && c.readyInstances !== null && c.instances !== null && c.readyInstances >= 1 && c.readyInstances === c.instances) {
    return { ...base, state: 'completed', tone: 'healthy', title: 'Restore completed', detail: `${c.readyInstances}/${c.instances} instances ready · ${c.phase}` }
  }

  const crashing = recoveryPods.map((p) => ({ p, c: failingContainer(p) })).find((x) => x.c)
  if (crashing) {
    return {
      ...base,
      state: 'progressing',
      tone: 'alert',
      title: `Recovery attempt failing in ${crashing.p.name}`,
      detail: `${crashing.c!.name}: ${[crashing.c!.reason, crashing.c!.message].filter(Boolean).join(' · ') || 'exited with an error'}. The Job retries; its logs say why.`,
      logsPod: { name: crashing.p.name, container: crashing.c!.name },
    }
  }
  const running = recoveryPods.find((p) => p.phase === 'Running' || p.phase === 'Pending')
  if (running) {
    const active = activeContainer(running)
    return {
      ...base,
      state: 'progressing',
      tone: 'neutral',
      title: 'Restoring the base backup and replaying WAL',
      detail: `${running.name}${active ? ` · ${active.name} ${active.state}` : ''}${c.phase ? ` · ${c.phase}` : ''}`,
      logsPod: { name: running.name, container: active?.name },
    }
  }
  if (!podsVisible && !c.phase) {
    return { ...base, state: 'unobservable', tone: 'unknown', title: 'Progress not visible', detail: snap.coverage.pods?.grant ? `Following the restore needs ${snap.coverage.pods.grant}` : 'The Cluster has not reported a phase yet' }
  }
  const starting = instances.find((p) => !p.ready)
  return {
    ...base,
    state: 'progressing',
    tone: 'neutral',
    title: recoveryPods.some((p) => p.phase === 'Succeeded') ? 'Recovery finished; starting the restored instances' : 'Waiting for the recovery to start',
    detail: [c.phase, c.readyInstances !== null && c.instances !== null ? `${c.readyInstances}/${c.instances} ready` : null, !podsVisible ? `Pods not visible${snap.coverage.pods?.grant ? ` (needs ${snap.coverage.pods.grant})` : ''}` : null]
      .filter(Boolean)
      .join(' · '),
    logsPod: starting ? { name: starting.name, container: 'postgres' } : undefined,
  }
}

/**
 * What the restore capability means for the dialog: `blocked` disables Review
 * with an alert (the grant, or the operator's webhook refusing writes);
 * `pending` only while it is being checked. A failed check blocks nothing: the
 * server dry-run at review still has the final say, and `unchecked` says so.
 */
export function restorePermission(
  namespace: string,
  cap: CNPGActionCapability | undefined,
  error: unknown,
): { blocked?: string; pending?: string; unchecked?: string } {
  if (cap) {
    if (cap.allowed) return {}
    if (cap.permission === 'denied') return { blocked: `Restoring creates a Cluster in ${namespace}, which needs ${cap.grant ?? 'create clusters (postgresql.cnpg.io)'}.` }
    return { blocked: cap.reason ?? 'Restoring is not available right now.' }
  }
  if (error) return { unchecked: `Whether you may create a Cluster in ${namespace} could not be checked (${error instanceof Error ? error.message : 'unknown error'}); the review step will tell.` }
  return { pending: `Checking whether you may create a Cluster in ${namespace}…` }
}

export type RestoreNextStepId = 'connect' | 'validate' | 'backup'

export interface RestoreNextStep {
  id: RestoreNextStepId
  label: string
  /** Set only where Radar can tell; `unknown` otherwise (whether applications moved is never known). */
  state: 'done' | 'partial' | 'todo' | 'unknown'
  note?: string
}

/**
 * What the Cluster spec declares for protection: WAL archiving (the Barman
 * plugin as WAL archiver with its ObjectStore named, or barmanObjectStore),
 * the plugin marked as WAL archiver but naming no ObjectStore to archive to,
 * base backups without WAL archiving (the plugin not marked isWALArchiver),
 * volume snapshots only, or nothing.
 */
export type RestoreBackupDeclared = 'walArchiving' | 'archiverWithoutDestination' | 'backupsNoArchiving' | 'snapshotsOnly' | 'none'

const BACKUP_STEP: Record<RestoreBackupDeclared | 'unread', Pick<RestoreNextStep, 'state' | 'note'>> = {
  unread: { state: 'unknown', note: 'Its backup configuration was not read' },
  walArchiving: { state: 'done', note: 'WAL archiving to an object store is configured' },
  archiverWithoutDestination: {
    state: 'partial',
    note: 'The Barman plugin is set as WAL archiver but names no ObjectStore (parameters.barmanObjectName), so WAL has nowhere to go',
  },
  backupsNoArchiving: { state: 'partial', note: 'An ObjectStore is declared, but not as the WAL archiver, so no point-in-time recovery' },
  snapshotsOnly: { state: 'partial', note: 'Volume snapshots declared, but no WAL archiving, so no point-in-time recovery' },
  none: { state: 'todo', note: 'It has no backup destination or WAL archiving yet' },
}

/**
 * The checklist a restored cluster shows once it is healthy. Links only;
 * Radar writes nothing. Validation reads the recorded note; backups read the
 * Cluster spec (undefined when not read). Snapshots alone are partial: they
 * give no point-in-time recovery.
 */
export function restoreNextSteps(input: { validationRecorded: boolean; backup: RestoreBackupDeclared | undefined }): RestoreNextStep[] {
  const label = 'Set up backups and WAL archiving'
  const backup: RestoreNextStep = { id: 'backup', label, ...BACKUP_STEP[input.backup ?? 'unread'] }
  return [
    { id: 'connect', label: 'Point applications at it', state: 'unknown', note: 'Radar cannot tell which applications use it' },
    input.validationRecorded
      ? { id: 'validate', label: 'Record what you checked', state: 'done', note: 'A validation note is recorded' }
      : { id: 'validate', label: 'Record what you checked', state: 'todo' },
    backup,
  ]
}

/** What a Cluster spec declares for backups and WAL archiving. */
export function restoreBackupDeclared(cluster: any): RestoreBackupDeclared {
  const plugin = getCNPGClusterBarmanPlugin(cluster)
  // plugin-barman-cloud reads the archive destination from the plugin's own
  // parameters; the recovery source's ObjectStore is a separate setting.
  if ((plugin?.isWALArchiver && plugin.barmanObjectName) || cluster?.spec?.backup?.barmanObjectStore?.destinationPath) return 'walArchiving'
  if (plugin?.isWALArchiver) return 'archiverWithoutDestination'
  if (plugin?.barmanObjectName) return 'backupsNoArchiving'
  if (cluster?.spec?.backup?.volumeSnapshot) return 'snapshotsOnly'
  return 'none'
}
