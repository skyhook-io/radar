// CloudNativePG workspace model: pure derivations over the /api/cnpg/workspace
// payload. Every fact here is something the cluster actually reports; when it
// does not report something the value is "unknown", never zero or healthy.

import type { HealthLevel } from '../resources/resource-utils'
import { formatBytes } from '../../utils/format'
import {
  CNPG_BARMAN_PLUGIN_NAME,
  getCNPGClusterBackupConfig,
  getCNPGClusterBarmanPlugin,
  getCNPGClusterImageTag,
  getCNPGClusterStatus,
  getCNPGObjectStoreRecoveryWindows,
  isApiGroup,
} from '../resources/resource-utils-cnpg'

export const CNPG_WORKSPACE_KEYS = [
  'clusters',
  'backups',
  'scheduledBackups',
  'poolers',
  'databases',
  'publications',
  'subscriptions',
  'databaseRoles',
  'imageCatalogs',
  'clusterImageCatalogs',
  'objectStores',
  'pods',
] as const

export type CNPGWorkspaceKey = (typeof CNPG_WORKSPACE_KEYS)[number]

export type CNPGCoverageState = 'full' | 'partial' | 'denied' | 'notInstalled' | 'syncing' | 'error'

export interface CNPGKindCoverage {
  state: CNPGCoverageState
  /** Denied namespaces, named only when the caller supplied the candidate list. */
  deniedNamespaces?: string[]
  /** For partial coverage: the namespaces that were read. */
  allowedNamespaces?: string[]
}

export interface CNPGWorkspaceIssue {
  id: string
  severity: 'critical' | 'warning'
  category?: string
  kind: string
  group?: string
  namespace?: string
  name: string
  reason: string
  message?: string
  cause?: string
  action?: string
  first_seen?: string
}

export interface CNPGAuditFinding {
  checkId: string
  severity: string
  kind: string
  group?: string
  namespace?: string
  name: string
  message: string
}

export interface CNPGWorkspaceResponse {
  installed: boolean
  context: string
  namespaces: string[] | null
  coverage: Partial<Record<CNPGWorkspaceKey, CNPGKindCoverage>>
  objects: Partial<Record<CNPGWorkspaceKey, any[]>>
  issues: CNPGWorkspaceIssue[]
  audit: CNPGAuditFinding[]
  backupsOmitted: number
}

export const CNPG_KIND_BY_KEY: Record<CNPGWorkspaceKey, { kind: string; group: string; plural: string }> = {
  clusters: { kind: 'Cluster', group: 'postgresql.cnpg.io', plural: 'clusters' },
  backups: { kind: 'Backup', group: 'postgresql.cnpg.io', plural: 'backups' },
  scheduledBackups: { kind: 'ScheduledBackup', group: 'postgresql.cnpg.io', plural: 'scheduledbackups' },
  poolers: { kind: 'Pooler', group: 'postgresql.cnpg.io', plural: 'poolers' },
  databases: { kind: 'Database', group: 'postgresql.cnpg.io', plural: 'databases' },
  publications: { kind: 'Publication', group: 'postgresql.cnpg.io', plural: 'publications' },
  subscriptions: { kind: 'Subscription', group: 'postgresql.cnpg.io', plural: 'subscriptions' },
  databaseRoles: { kind: 'DatabaseRole', group: 'postgresql.cnpg.io', plural: 'databaseroles' },
  imageCatalogs: { kind: 'ImageCatalog', group: 'postgresql.cnpg.io', plural: 'imagecatalogs' },
  clusterImageCatalogs: { kind: 'ClusterImageCatalog', group: 'postgresql.cnpg.io', plural: 'clusterimagecatalogs' },
  objectStores: { kind: 'ObjectStore', group: 'barmancloud.cnpg.io', plural: 'objectstores' },
  pods: { kind: 'Pod', group: '', plural: 'pods' },
}

export function isCNPGWorkspaceKind(kind: string, group: string | undefined): boolean {
  return Object.values(CNPG_KIND_BY_KEY).some((k) => k.group !== '' && k.group === (group ?? '') && k.kind === kind)
}

/** The value is observed, derived, or not available from the cluster. */
export type CNPGFactTone = HealthLevel

export interface CNPGFact {
  text: string
  tone: CNPGFactTone
  /** Where the value comes from, shown next to it so claims carry their source. */
  source?: string
  /** A timestamp the text refers to; the UI renders it as an age. */
  at?: string
}

export type CNPGProblemCategory = 'availability' | 'protection' | 'declarations' | 'pooling'

export const CNPG_PROBLEM_CATEGORIES: { id: CNPGProblemCategory; label: string }[] = [
  { id: 'availability', label: 'Availability' },
  { id: 'protection', label: 'Protection' },
  { id: 'declarations', label: 'Declarations' },
  { id: 'pooling', label: 'Pooling' },
]

export interface CNPGProblem {
  /** Stable identity for keys. */
  id: string
  severity: 'critical' | 'warning' | 'posture'
  category: CNPGProblemCategory
  title: string
  detail?: string
  /** The object the evidence is about (may be the Cluster or a child object). */
  subject: { kind: string; group: string; namespace: string; name: string }
  /**
   * measurement: derived here from a reading only callers holding its grants
   * receive (disk use, instance Pod readiness).
   */
  source: 'issue' | 'audit' | 'measurement'
}

export interface CNPGInstance {
  name: string
  role: 'primary' | 'replica' | 'unknown'
  ready: boolean | null
  node?: string
  zone?: string
}

export interface CNPGProtectionFacts {
  schedule: CNPGFact & { names: string[] }
  destination: CNPGFact & {
    method: 'plugin' | 'barmanObjectStore' | 'volumeSnapshot' | 'none'
    objectStore?: string
  }
  lastSuccessfulBackup: CNPGFact
  walArchiving: CNPGFact
  recoveryWindow: CNPGFact & { from?: string; to?: string }
  restoreValidation: CNPGFact & { restoredInto?: { namespace: string; name: string } }
}

export interface CNPGFleetRow {
  key: string
  namespace: string
  name: string
  cluster: any
  controllerStatus: { text: string; level: HealthLevel }
  instances: { ready: number | null; desired: number | null }
  /**
   * Ready instances counted from the instance Pods' Ready condition; absent
   * when Pods are not readable here or any Pod's readiness is unknown.
   */
  podReadiness?: { ready: number; total: number }
  /** status.readyInstances claims more ready instances than the Pods show: CNPG status is stale or lagging. */
  readinessContradicted?: boolean
  /** status.currentPrimary is not the Pod labelled primary; status may be stale, or a failover is under way. */
  primaryConflict?: { status: string; labelled: string }
  pods: CNPGInstance[]
  replicaCluster: { source?: string } | null
  hibernated: boolean
  pgVersion: string | null
  catalog: { kind: string; name: string } | null
  replication: CNPGFact
  protection: CNPGProtectionFacts & { summary: CNPGFact }
  declarations: { summary: CNPGFact; total: number; failed: number; pending: number }
  poolers: string[]
  /** The Pooler objects behind `poolers`, for their type and Service port. */
  poolerObjects?: any[]
  /** False when Poolers are not readable in this cluster's namespace, so an empty list means unknown. */
  poolersKnown: boolean
  problems: CNPGProblem[]
  /** Any issue at warning or worse. Posture findings alone do not need attention. */
  attention: boolean
  categories: Set<CNPGProblemCategory>
  /** GitOps owner recorded on the Cluster, when it carries the standard labels. */
  gitops: CNPGGitOpsSource | null
  /** Fullest measured volume, set by applyCNPGDisk; absent when no disk reading was requested. */
  disk?: CNPGFact
  /** Growth of the fastest-growing volume, set by applyCNPGFleetMetrics when measured. */
  diskGrowth?: CNPGFact
}

export interface CNPGFleet {
  rows: CNPGFleetRow[]
  attentionCount: number
  categoryCounts: Record<CNPGProblemCategory, number>
  /** Kinds whose coverage is not complete, so counts built on them are lower bounds. */
  incompleteKinds: CNPGWorkspaceKey[]
}

const PROTECTION_ISSUE_REASONS = new Set([
  'CNPGWALArchivingFailing',
  'CNPGLastBackupFailed',
  'CNPGBackupFailed',
  'CNPGScheduledBackupMissed',
  'CNPGScheduledRunNoBackup',
])

export function cnpgIssueCategory(issue: Pick<CNPGWorkspaceIssue, 'kind' | 'reason'>): CNPGProblemCategory {
  if (PROTECTION_ISSUE_REASONS.has(issue.reason)) return 'protection'
  switch (issue.kind) {
    case 'Backup':
    case 'ScheduledBackup':
    case 'ObjectStore':
      return 'protection'
    case 'Database':
    case 'Publication':
    case 'Subscription':
    case 'DatabaseRole':
      return 'declarations'
    case 'Pooler':
      return 'pooling'
    default:
      return 'availability'
  }
}

function key(ns: string | undefined, name: string): string {
  return `${ns ?? ''}/${name}`
}

function specClusterName(obj: any): string | undefined {
  const n = obj?.spec?.cluster?.name
  return typeof n === 'string' && n ? n : undefined
}

function coverageOf(resp: CNPGWorkspaceResponse, k: CNPGWorkspaceKey): CNPGKindCoverage {
  return resp.coverage?.[k] ?? { state: 'notInstalled' }
}

/** Coverage is usable in a namespace when that namespace's objects were read. */
export function coverageReadable(cov: CNPGKindCoverage, namespace?: string): boolean {
  if (cov.state === 'full') return true
  if (cov.state === 'partial') {
    if (!namespace) return false
    if (cov.allowedNamespaces) return cov.allowedNamespaces.includes(namespace)
    if (cov.deniedNamespaces) return !cov.deniedNamespaces.includes(namespace)
    return false
  }
  return false
}

function coverageUnavailableText(cov: CNPGKindCoverage, what: string): string {
  switch (cov.state) {
    case 'denied':
    case 'partial':
      return `No access to ${what}`
    case 'syncing':
      return 'Loading…'
    case 'error':
      return `Could not read ${what}`
    default:
      return 'Not installed'
  }
}

function timeOf(obj: any): number {
  const t = obj?.status?.stoppedAt || obj?.status?.startedAt || obj?.metadata?.creationTimestamp
  const ms = t ? Date.parse(t) : NaN
  return Number.isFinite(ms) ? ms : 0
}

function podRole(pod: any, cluster: any): CNPGInstance['role'] {
  const role = pod?.metadata?.labels?.['cnpg.io/instanceRole'] ?? pod?.metadata?.labels?.role
  if (role === 'primary') return 'primary'
  if (role === 'replica') return 'replica'
  const primary = cluster?.status?.currentPrimary
  if (primary && pod?.metadata?.name === primary) return 'primary'
  return 'unknown'
}

function podReady(pod: any): boolean | null {
  const conds = pod?.status?.conditions
  if (!Array.isArray(conds)) return null
  const ready = conds.find((c: any) => c?.type === 'Ready')
  if (!ready) return null
  return ready.status === 'True'
}

export type CNPGGitOpsSource = { tool: 'argocd' | 'flux'; name: string; namespace?: string }

/** The GitOps owner recorded on an object's standard Argo CD / Flux labels. */
export function cnpgGitOpsSource(obj: any): CNPGGitOpsSource | null {
  const labels = obj?.metadata?.labels ?? {}
  const annotations = obj?.metadata?.annotations ?? {}
  const argo = labels['argocd.argoproj.io/instance']
  if (argo) return { tool: 'argocd', name: argo }
  const tracking = annotations['argocd.argoproj.io/tracking-id']
  if (typeof tracking === 'string' && tracking.includes(':')) {
    return { tool: 'argocd', name: tracking.split(':')[0] }
  }
  const fluxName = labels['kustomize.toolkit.fluxcd.io/name'] || labels['helm.toolkit.fluxcd.io/name']
  if (fluxName) {
    return {
      tool: 'flux',
      name: fluxName,
      namespace: labels['kustomize.toolkit.fluxcd.io/namespace'] || labels['helm.toolkit.fluxcd.io/namespace'],
    }
  }
  return null
}

function scheduleFact(
  cluster: any,
  schedules: any[],
  cov: CNPGKindCoverage,
): CNPGProtectionFacts['schedule'] {
  const ns = cluster.metadata?.namespace
  if (!coverageReadable(cov, ns)) {
    return { text: coverageUnavailableText(cov, 'ScheduledBackups'), tone: 'unknown', names: [] }
  }
  const mine = schedules.filter((s) => s.metadata?.namespace === ns && specClusterName(s) === cluster.metadata?.name)
  if (mine.length === 0) return { text: 'No declarative schedule', tone: 'neutral', names: [] }
  const active = mine.filter((s) => s.spec?.suspend !== true)
  const names = mine.map((s) => s.metadata?.name).filter(Boolean)
  if (active.length === 0) {
    return { text: mine.length === 1 ? 'Schedule suspended' : 'All schedules suspended', tone: 'degraded', names }
  }
  const cron = active[0]?.spec?.schedule
  return {
    text: active.length === 1 ? (cron ? `Scheduled · ${cron}` : 'Scheduled') : `${active.length} schedules`,
    tone: 'healthy',
    names,
  }
}

function destinationFact(cluster: any): CNPGProtectionFacts['destination'] {
  const plugin = getCNPGClusterBarmanPlugin(cluster)
  if (plugin?.barmanObjectName) {
    return {
      text: `ObjectStore ${plugin.barmanObjectName}`,
      tone: 'neutral',
      method: 'plugin',
      objectStore: plugin.barmanObjectName,
    }
  }
  const cfg = getCNPGClusterBackupConfig(cluster)
  if (cfg.destinationPath) {
    return { text: cfg.destinationPath, tone: 'neutral', method: 'barmanObjectStore' }
  }
  if (cluster?.spec?.backup?.volumeSnapshot) {
    return { text: 'Volume snapshots', tone: 'neutral', method: 'volumeSnapshot' }
  }
  return { text: 'No destination configured', tone: 'neutral', method: 'none' }
}

function recoveryWindowFor(cluster: any, stores: any[]): { from?: string; lastSuccess?: string; lastFailed?: string; store?: string } | null {
  const plugin = getCNPGClusterBarmanPlugin(cluster)
  if (!plugin?.barmanObjectName) return null
  const store = stores.find(
    (s) => s.metadata?.namespace === cluster.metadata?.namespace && s.metadata?.name === plugin.barmanObjectName,
  )
  if (!store) return null
  const server = plugin.serverName || cluster.metadata?.name
  const w = getCNPGObjectStoreRecoveryWindows(store).find((x) => x.server === server)
  if (!w) return { store: store.metadata?.name }
  return {
    from: w.firstRecoverabilityPoint,
    lastSuccess: w.lastSuccessfulBackupTime,
    lastFailed: w.lastFailedBackupTime,
    store: store.metadata?.name,
  }
}

function lastBackupFact(
  cluster: any,
  backups: any[],
  backupsCov: CNPGKindCoverage,
  window: ReturnType<typeof recoveryWindowFor>,
  storesUnreadable: CNPGKindCoverage | null,
): CNPGProtectionFacts['lastSuccessfulBackup'] {
  const ns = cluster.metadata?.namespace
  const name = cluster.metadata?.name
  const candidates: { at: string; source: string }[] = []
  if (coverageReadable(backupsCov, ns)) {
    const completed = backups
      .filter(
        (b) =>
          b.metadata?.namespace === ns &&
          specClusterName(b) === name &&
          isApiGroup(b.apiVersion, 'postgresql.cnpg.io') &&
          b.status?.phase === 'completed',
      )
      .sort((a, b) => timeOf(b) - timeOf(a))[0]
    const at = completed?.status?.stoppedAt || completed?.status?.startedAt
    if (completed && at) candidates.push({ at, source: `Backup ${completed.metadata?.name}` })
  }
  if (window?.lastSuccess) candidates.push({ at: window.lastSuccess, source: `ObjectStore ${window.store} status` })
  const cfg = getCNPGClusterBackupConfig(cluster)
  if (!cfg.plugin && cfg.lastSuccessfulBackup) candidates.push({ at: cfg.lastSuccessfulBackup, source: 'Cluster status' })
  if (candidates.length === 0) {
    if (!coverageReadable(backupsCov, ns)) {
      return { text: coverageUnavailableText(backupsCov, 'Backups'), tone: 'unknown' }
    }
    if (storesUnreadable) return { text: coverageUnavailableText(storesUnreadable, 'ObjectStores'), tone: 'unknown' }
    return { text: 'None observed', tone: 'unknown' }
  }
  const best = candidates.reduce((a, b) => (Date.parse(a.at) >= Date.parse(b.at) ? a : b))
  return { text: 'Completed', tone: 'healthy', at: best.at, source: best.source }
}

function walFact(cluster: any): CNPGFact {
  const conds = cluster?.status?.conditions
  const c = Array.isArray(conds) ? conds.find((x: any) => x?.type === 'ContinuousArchiving') : null
  if (!c) return { text: 'Not reported', tone: 'unknown', source: 'Cluster status' }
  if (c.status === 'True') return { text: 'Archiving', tone: 'healthy', source: 'ContinuousArchiving condition' }
  if (c.status === 'False') {
    return { text: c.message ? `Failing · ${c.message}` : 'Failing', tone: 'unhealthy', source: 'ContinuousArchiving condition' }
  }
  return { text: 'Unknown', tone: 'unknown', source: 'ContinuousArchiving condition' }
}

export const CNPG_RESTORE_VALIDATION_ANNOTATION = 'radar.skyhook.io/restore-validation'

export interface CNPGRestoreValidationNote {
  recordedAt: string
  recordedBy?: string
  checked: string
  targetTime?: string
  source?: { namespace: string; name: string; uid?: string; verified: boolean }
  target?: { namespace: string; name: string; uid?: string; verified: boolean }
}

/** The validation note recorded on a restored Cluster, or null when absent or malformed. */
export function getCNPGRestoreValidation(cluster: any): CNPGRestoreValidationNote | null {
  const raw = cluster?.metadata?.annotations?.[CNPG_RESTORE_VALIDATION_ANNOTATION]
  if (typeof raw !== 'string' || !raw.trim()) return null
  try {
    const v = JSON.parse(raw)
    if (typeof v?.recordedAt !== 'string' || typeof v?.checked !== 'string' || !v.checked) return null
    return v as CNPGRestoreValidationNote
  } catch {
    return null
  }
}

// A note counts for this source only when it names this Cluster: by UID when
// the recorder could read it, otherwise by name.
function noteIsAbout(note: CNPGRestoreValidationNote, cluster: any): boolean {
  const src = note.source
  if (!src) return false
  if (src.uid) return src.uid === cluster.metadata?.uid
  return src.namespace === cluster.metadata?.namespace && src.name === cluster.metadata?.name
}

function restoreValidationFact(
  cluster: any,
  allClusters: any[],
  backups: any[],
): CNPGProtectionFacts['restoreValidation'] {
  const plugin = getCNPGClusterBarmanPlugin(cluster)
  const server = plugin?.serverName || cluster.metadata?.name
  const store = plugin?.barmanObjectName
  const ns = cluster.metadata?.namespace
  const name = cluster.metadata?.name
  const restoredFromThis = allClusters.filter((c) => {
    if (c === cluster || c.metadata?.namespace !== ns) return false
    const recovery = c.spec?.bootstrap?.recovery
    if (!recovery) return false
    const sourceName = recovery.source
    if (sourceName) {
      const ext = (c.spec?.externalClusters ?? []).find((e: any) => e?.name === sourceName)
      const params = ext?.plugin?.name === CNPG_BARMAN_PLUGIN_NAME ? ext.plugin.parameters : undefined
      if (store && params?.barmanObjectName === store && (params?.serverName || sourceName) === server) return true
    }
    const backupName = recovery.backup?.name
    if (!backupName) return false
    const backup = backups.find((b) => b.metadata?.namespace === ns && b.metadata?.name === backupName)
    return specClusterName(backup) === name
  })
  const noted = restoredFromThis
    .map((c) => ({ c, note: getCNPGRestoreValidation(c) }))
    .filter((x): x is { c: any; note: CNPGRestoreValidationNote } => !!x.note && noteIsAbout(x.note, cluster))
    .sort((a, b) => Date.parse(b.note.recordedAt) - Date.parse(a.note.recordedAt))[0]
  if (noted) {
    const rname = noted.c.metadata?.name
    const by = noted.note.recordedBy ? `by ${noted.note.recordedBy}` : 'by a user Radar could not identify'
    return {
      text: 'Validation recorded',
      tone: 'neutral',
      at: noted.note.recordedAt,
      source: `Recorded ${by} on ${rname}${noted.note.targetTime ? ` (target ${noted.note.targetTime})` : ''}: ${noted.note.checked.length > 140 ? `${noted.note.checked.slice(0, 140)}…` : noted.note.checked}. A person's note, not a check Radar ran.`,
      restoredInto: { namespace: noted.c.metadata?.namespace, name: rname },
    }
  }
  const restored = restoredFromThis[0]
  if (!restored) return { text: 'None recorded', tone: 'unknown', source: 'Kubernetes does not record restore tests' }
  const rname = restored.metadata?.name
  const ready = typeof restored.status?.readyInstances === 'number' && restored.status.readyInstances > 0
  if (!ready) {
    return {
      text: `Recovery declared in ${rname}`,
      tone: 'unknown',
      source: `Cluster ${rname} bootstraps from this cluster's backups but has no ready instance yet`,
      restoredInto: { namespace: restored.metadata?.namespace, name: rname },
    }
  }
  return {
    text: `Restored into ${rname}`,
    tone: 'neutral',
    source: `Cluster ${rname} bootstrapped from this cluster's backups and has ready instances · created ${restored.metadata?.creationTimestamp ?? 'unknown'}. No validation note is recorded on it; this proves one recovery, not that today's backups restore.`,
    restoredInto: { namespace: restored.metadata?.namespace, name: rname },
  }
}

function protectionSummary(p: CNPGProtectionFacts): CNPGFact {
  if (p.walArchiving.tone === 'unhealthy') return { text: 'WAL archiving failing', tone: 'unhealthy' }
  if (p.destination.method === 'none' && p.schedule.names.length === 0 && p.schedule.tone !== 'unknown') {
    return { text: 'No backup destination or schedule', tone: 'neutral' }
  }
  if (p.schedule.tone === 'degraded') return { text: p.schedule.text, tone: 'degraded' }
  if (p.lastSuccessfulBackup.at) return { text: 'Last backup', tone: 'healthy', at: p.lastSuccessfulBackup.at }
  return { text: p.lastSuccessfulBackup.text, tone: p.lastSuccessfulBackup.tone }
}

function catalogRef(cluster: any): CNPGFleetRow['catalog'] {
  const ref = cluster?.spec?.imageCatalogRef
  if (!ref?.name) return null
  return { kind: ref.kind || 'ImageCatalog', name: ref.name }
}

function pgVersion(cluster: any): string | null {
  const tag = getCNPGClusterImageTag(cluster)
  if (tag && tag !== '-') {
    const m = tag.match(/^(\d+(?:\.\d+)?)/)
    if (m) return m[1]
  }
  const major = cluster?.spec?.imageCatalogRef?.major
  return typeof major === 'number' ? String(major) : null
}

function replicationFact(cluster: any, pods: CNPGInstance[], hibernated: boolean, podsCov: CNPGKindCoverage): CNPGFact {
  if (hibernated) return { text: 'Hibernated', tone: 'neutral' }
  const desired = cluster?.spec?.instances
  if (desired === 1) return { text: 'Single instance', tone: 'neutral' }
  if (!coverageReadable(podsCov, cluster?.metadata?.namespace)) {
    return { text: coverageUnavailableText(podsCov, 'Pods'), tone: 'unknown' }
  }
  const replicas = pods.filter((p) => p.role === 'replica')
  const readyReplicas = replicas.filter((p) => p.ready === true).length
  if (replicas.length === 0) return { text: 'No replica pods observed', tone: 'unknown' }
  return {
    text: `${readyReplicas}/${replicas.length} replicas ready · lag unknown`,
    tone: 'unknown',
    source: CNPG_LAG_UNMEASURED_SOURCE,
  }
}

function problemsFor(
  cluster: any,
  issues: CNPGWorkspaceIssue[],
  audit: CNPGAuditFinding[],
  children: Map<string, string>,
): CNPGProblem[] {
  const ns = cluster.metadata?.namespace
  const name = cluster.metadata?.name
  const out: CNPGProblem[] = []
  for (const issue of issues) {
    if ((issue.namespace ?? '') !== ns) continue
    const isSelf = issue.kind === 'Cluster' && issue.name === name
    const owner = children.get(`${issue.kind}/${ns}/${issue.name}`)
    if (!isSelf && owner !== name) continue
    out.push({
      id: `${issue.id}:${issue.kind}/${issue.name}`,
      severity: issue.severity,
      category: cnpgIssueCategory(issue),
      title: issue.message || issue.reason,
      detail: issue.cause || undefined,
      subject: { kind: issue.kind, group: issue.group ?? '', namespace: ns, name: issue.name },
      source: 'issue',
    })
  }
  for (const f of audit) {
    if (f.kind !== 'Cluster' || f.name !== name || (f.namespace ?? '') !== ns) continue
    out.push({
      id: `audit:${f.checkId}:${ns}/${name}`,
      severity: 'posture',
      category: 'protection',
      title: f.checkId === 'cnpgNoDeclarativeBackup' ? 'No declarative backup schedule' : f.message,
      detail: f.message,
      subject: { kind: 'Cluster', group: 'postgresql.cnpg.io', namespace: ns, name },
      source: 'audit',
    })
  }
  const rank = { critical: 0, warning: 1, posture: 2 } as const
  return out.sort((a, b) => rank[a.severity] - rank[b.severity] || a.title.localeCompare(b.title))
}

/**
 * The ready count to show for a cluster: CNPG's status count, or the Pods'
 * own count when the status claims more than the Pods show.
 */
export function cnpgReadyInstances(row: Pick<CNPGFleetRow, 'instances' | 'podReadiness' | 'readinessContradicted'>): { text: string; tone?: HealthLevel; note?: string } {
  const desired = row.instances.desired ?? '–'
  if (row.readinessContradicted && row.podReadiness) {
    return {
      text: `${row.podReadiness.ready}/${desired}`,
      tone: row.podReadiness.ready === 0 ? 'unhealthy' : 'degraded',
      note: `Counted from the instance Pods' Ready condition. CNPG status reports ${row.instances.ready} ready, so the status may be stale.`,
    }
  }
  return { text: `${row.instances.ready ?? '–'}/${desired}` }
}

function sortProblems(list: CNPGProblem[]): CNPGProblem[] {
  const rank = { critical: 0, warning: 1, posture: 2 } as const
  return list.sort((a, b) => rank[a.severity] - rank[b.severity] || a.title.localeCompare(b.title))
}

function primaryConflictOf(cluster: any, pods: any[]): CNPGFleetRow['primaryConflict'] {
  const status = cluster?.status?.currentPrimary
  if (!status) return undefined
  const labelled = pods
    .filter((p) => (p?.metadata?.labels?.['cnpg.io/instanceRole'] ?? p?.metadata?.labels?.role) === 'primary')
    .map((p) => p.metadata?.name as string)
  if (labelled.length === 0 || labelled.includes(status)) return undefined
  return { status, labelled: labelled.sort()[0] }
}

/**
 * Availability problems Radar observes on the instance Pods when CNPG status
 * says otherwise — the status is the operator's last word and goes stale
 * while it is not reconciling. Worded as what is observed.
 */
function observedProblems(
  cluster: any,
  pods: CNPGInstance[],
  contradicted: { ready: number; total: number } | undefined,
  statusReady: number | null,
  conflict: CNPGFleetRow['primaryConflict'],
): CNPGProblem[] {
  const ns = cluster.metadata?.namespace
  const name = cluster.metadata?.name
  const subject = { kind: 'Cluster', group: 'postgresql.cnpg.io', namespace: ns, name }
  const out: CNPGProblem[] = []
  if (contradicted) {
    const primaryDown = pods.some((p) => p.role === 'primary' && p.ready === false)
    const notReady = contradicted.total - contradicted.ready
    out.push({
      id: `pods-not-ready:${ns}/${name}`,
      severity: primaryDown || contradicted.ready === 0 ? 'critical' : 'warning',
      category: 'availability',
      title: `${notReady} of ${contradicted.total} instance Pods not ready${primaryDown ? ', including the primary' : ''}`,
      detail: `The Pods' Ready condition shows ${contradicted.ready} ready; CNPG status still reports ${statusReady}. The status may be stale.`,
      subject,
      source: 'measurement',
    })
  }
  if (conflict) {
    out.push({
      id: `primary-conflict:${ns}/${name}`,
      severity: 'warning',
      category: 'availability',
      title: `CNPG status names ${conflict.status} primary; the Pod labelled primary is ${conflict.labelled}`,
      detail: 'Status may be stale, or a failover is under way.',
      subject,
      source: 'measurement',
    })
  }
  return out
}

/** Index "Kind/ns/name" → owning cluster name, from each child's spec.cluster.name. */
function childIndex(resp: CNPGWorkspaceResponse): Map<string, string> {
  const idx = new Map<string, string>()
  const add = (kind: string, list: any[] | undefined) => {
    for (const o of list ?? []) {
      const c = specClusterName(o)
      if (c) idx.set(`${kind}/${o.metadata?.namespace}/${o.metadata?.name}`, c)
    }
  }
  add('Backup', resp.objects.backups)
  add('ScheduledBackup', resp.objects.scheduledBackups)
  add('Pooler', resp.objects.poolers)
  add('Database', resp.objects.databases)
  add('Publication', resp.objects.publications)
  add('Subscription', resp.objects.subscriptions)
  add('DatabaseRole', resp.objects.databaseRoles)
  for (const p of resp.objects.pods ?? []) {
    const c = p?.metadata?.labels?.['cnpg.io/cluster']
    if (c) idx.set(`Pod/${p.metadata?.namespace}/${p.metadata?.name}`, c)
  }
  return idx
}

function declarationsFor(cluster: any, resp: CNPGWorkspaceResponse): CNPGFleetRow['declarations'] {
  const ns = cluster.metadata?.namespace
  const name = cluster.metadata?.name
  const lists: [CNPGWorkspaceKey, any[]][] = [
    ['databases', resp.objects.databases ?? []],
    ['publications', resp.objects.publications ?? []],
    ['subscriptions', resp.objects.subscriptions ?? []],
    ['databaseRoles', resp.objects.databaseRoles ?? []],
  ]
  let total = 0
  let failed = 0
  let pending = 0
  let unreadable = false
  for (const [k, list] of lists) {
    if (!coverageReadable(coverageOf(resp, k), ns)) {
      if (coverageOf(resp, k).state !== 'notInstalled') unreadable = true
      continue
    }
    for (const o of list) {
      if (o.metadata?.namespace !== ns || specClusterName(o) !== name) continue
      total++
      if (o.status?.applied === false) failed++
      else if (o.status?.applied !== true) pending++
    }
  }
  const roleStatus = cluster?.status?.managedRolesStatus
  const reconciledRoles = new Set<string>(roleStatus?.byStatus?.reconciled ?? [])
  const failedRoles = new Set<string>(Object.keys(roleStatus?.cannotReconcile ?? {}))
  const declaredRoles: any[] = Array.isArray(cluster?.spec?.managed?.roles) ? cluster.spec.managed.roles : []
  for (const r of declaredRoles) {
    if (!r?.name) continue
    total++
    if (failedRoles.has(r.name)) failed++
    else if (!reconciledRoles.has(r.name)) pending++
  }
  let summary: CNPGFact
  if (total === 0) {
    summary = unreadable ? { text: 'No access to some declarations', tone: 'unknown' } : { text: 'None declared', tone: 'neutral' }
  } else if (failed > 0) {
    summary = { text: `${failed} of ${total} not reconciled`, tone: 'degraded' }
  } else if (pending > 0) {
    summary = { text: `${pending} of ${total} pending`, tone: 'unknown' }
  } else {
    summary = { text: `${total} reconciled`, tone: 'healthy' }
  }
  if (unreadable && total > 0) summary = { ...summary, source: 'Some declaration kinds are not readable' }
  return { summary, total, failed, pending }
}

export function buildCNPGFleet(resp: CNPGWorkspaceResponse): CNPGFleet {
  const clusters = (resp.objects.clusters ?? []).filter((c) => isApiGroup(c?.apiVersion, 'postgresql.cnpg.io'))
  const pods = resp.objects.pods ?? []
  const stores = resp.objects.objectStores ?? []
  const children = childIndex(resp)
  const poolers = resp.objects.poolers ?? []

  const rows: CNPGFleetRow[] = clusters.map((cluster) => {
    const ns = cluster.metadata?.namespace ?? ''
    const name = cluster.metadata?.name ?? ''
    const status = getCNPGClusterStatus(cluster)
    const hibernated = cluster?.metadata?.annotations?.['cnpg.io/hibernation'] === 'on'
    const instancePods: CNPGInstance[] = pods
      .filter((p) => p.metadata?.namespace === ns && p.metadata?.labels?.['cnpg.io/cluster'] === name)
      .map((p) => ({
        name: p.metadata?.name,
        role: podRole(p, cluster),
        ready: podReady(p),
        node: p.spec?.nodeName,
      }))
      .sort((a, b) => (a.role === 'primary' ? -1 : b.role === 'primary' ? 1 : a.name.localeCompare(b.name)))
    const readyInstances = typeof cluster?.status?.readyInstances === 'number' ? cluster.status.readyInstances : null
    const desired = typeof cluster?.spec?.instances === 'number' ? cluster.spec.instances : null

    const window = recoveryWindowFor(cluster, stores)
    const storesCov = coverageOf(resp, 'objectStores')
    const storesUnreadable = !!getCNPGClusterBarmanPlugin(cluster)?.barmanObjectName && !coverageReadable(storesCov, ns)
    const protection: CNPGProtectionFacts = {
      schedule: scheduleFact(cluster, resp.objects.scheduledBackups ?? [], coverageOf(resp, 'scheduledBackups')),
      destination: destinationFact(cluster),
      lastSuccessfulBackup: lastBackupFact(cluster, resp.objects.backups ?? [], coverageOf(resp, 'backups'), window, storesUnreadable ? storesCov : null),
      walArchiving: walFact(cluster),
      recoveryWindow: window?.from
        ? {
            text: 'Recoverable window',
            tone: window.lastFailed && (!window.lastSuccess || Date.parse(window.lastFailed) > Date.parse(window.lastSuccess)) ? 'degraded' : 'neutral',
            from: window.from,
            to: window.lastSuccess,
            source: `ObjectStore ${window.store} status`,
          }
        : storesUnreadable
          ? { text: coverageUnavailableText(storesCov, 'ObjectStores'), tone: 'unknown' }
          : { text: 'Not reported', tone: 'unknown' },
      restoreValidation: restoreValidationFact(cluster, clusters, resp.objects.backups ?? []),
    }
    const podsReadable = coverageReadable(coverageOf(resp, 'pods'), ns)
    const podReadiness =
      podsReadable && instancePods.length > 0 && instancePods.every((p) => p.ready !== null)
        ? { ready: instancePods.filter((p) => p.ready).length, total: instancePods.length }
        : undefined
    const readinessContradicted = !hibernated && !!podReadiness && readyInstances !== null && podReadiness.ready < readyInstances
    const primaryConflict = podsReadable ? primaryConflictOf(cluster, pods.filter((p) => p.metadata?.namespace === ns && p.metadata?.labels?.['cnpg.io/cluster'] === name)) : undefined
    const problems = sortProblems([
      ...problemsFor(cluster, resp.issues ?? [], resp.audit ?? [], children),
      ...observedProblems(cluster, instancePods, readinessContradicted ? podReadiness : undefined, readyInstances, primaryConflict),
    ])
    const categories = new Set<CNPGProblemCategory>(
      problems.filter((p) => p.severity !== 'posture').map((p) => p.category),
    )
    const replica = cluster?.spec?.replica?.enabled ? { source: cluster.spec.replica.source } : null

    return {
      key: key(ns, name),
      namespace: ns,
      name,
      cluster,
      controllerStatus: { text: status.text, level: status.level },
      instances: { ready: readyInstances, desired },
      ...(podReadiness ? { podReadiness } : {}),
      ...(readinessContradicted ? { readinessContradicted } : {}),
      ...(primaryConflict ? { primaryConflict } : {}),
      pods: instancePods,
      replicaCluster: replica,
      hibernated,
      pgVersion: pgVersion(cluster),
      catalog: catalogRef(cluster),
      replication: replicationFact(cluster, instancePods, hibernated, coverageOf(resp, 'pods')),
      protection: { ...protection, summary: protectionSummary(protection) },
      declarations: declarationsFor(cluster, resp),
      poolerObjects: poolers.filter((p) => p.metadata?.namespace === ns && specClusterName(p) === name),
      poolers: poolers
        .filter((p) => p.metadata?.namespace === ns && specClusterName(p) === name)
        .map((p) => p.metadata?.name),
      poolersKnown: coverageReadable(coverageOf(resp, 'poolers'), ns),
      problems,
      attention: problems.some((p) => p.severity !== 'posture'),
      categories,
      gitops: cnpgGitOpsSource(cluster),
    }
  })

  const incompleteKinds = CNPG_WORKSPACE_KEYS.filter((k) => {
    const s = coverageOf(resp, k).state
    return s === 'partial' || s === 'denied' || s === 'syncing' || s === 'error'
  })

  return finishFleet(rows, incompleteKinds)
}

function urgencyOf(row: CNPGFleetRow): { worst: number; urgent: number; total: number } {
  let worst = 3
  let urgent = 0
  for (const p of row.problems) {
    worst = Math.min(worst, PROBLEM_RANK[p.severity])
    if (p.severity !== 'posture') urgent++
  }
  return { worst, urgent, total: row.problems.length }
}

/**
 * Worst problem first (critical, warning, posture, none), then the most
 * attention-level problems, then all problems, then namespace/name — the same
 * order in every filter, so a row never jumps when the filter changes.
 */
export function compareCNPGFleetUrgency(a: CNPGFleetRow, b: CNPGFleetRow): number {
  const ua = urgencyOf(a)
  const ub = urgencyOf(b)
  return (
    ua.worst - ub.worst ||
    ub.urgent - ua.urgent ||
    ub.total - ua.total ||
    a.namespace.localeCompare(b.namespace) ||
    a.name.localeCompare(b.name)
  )
}

function finishFleet(rows: CNPGFleetRow[], incompleteKinds: CNPGWorkspaceKey[]): CNPGFleet {
  rows.sort(compareCNPGFleetUrgency)
  const categoryCounts = { availability: 0, protection: 0, declarations: 0, pooling: 0 } as Record<CNPGProblemCategory, number>
  for (const r of rows) for (const c of r.categories) categoryCounts[c]++
  return {
    rows,
    attentionCount: rows.filter((r) => r.attention).length,
    categoryCounts,
    incompleteKinds,
  }
}

/** One cluster's answer from /api/cnpg/disk. */
export interface CNPGDiskReading {
  namespace: string
  name: string
  /** ok | partial | noSeries | noPrometheus | denied | unavailable | error | notRead | ambiguous | scopeMismatch */
  state: string
  grant?: string
  reason?: string
  claims: number
  measured: number
  max?: {
    claim: string
    instance: string
    role: string
    tablespace?: string
    usedBytes: number
    capacityBytes: number
    ratio: number
  }
}

export const CNPG_DISK_WARNING_RATIO = 0.8
export const CNPG_DISK_CRITICAL_RATIO = 0.9

export const CNPG_DISK_SOURCE = 'kubelet volume stats via Prometheus'

export function cnpgVolumeRoleLabel(role: string, tablespace?: string): string {
  switch (role) {
    case 'PG_DATA':
      return 'data volume'
    case 'PG_WAL':
      return 'WAL volume'
    case 'PG_TABLESPACE':
      return tablespace ? `tablespace ${tablespace} volume` : 'tablespace volume'
    default:
      return 'volume'
  }
}

export function cnpgDiskTone(ratio: number): HealthLevel {
  if (ratio >= CNPG_DISK_CRITICAL_RATIO) return 'unhealthy'
  if (ratio >= CNPG_DISK_WARNING_RATIO) return 'degraded'
  return 'healthy'
}

/** The fleet and summary "Storage" fact: the fullest measured volume, or why there is none. */
export function cnpgDiskFact(r: CNPGDiskReading | undefined): CNPGFact {
  if (!r) return { text: 'Not read', tone: 'unknown' }
  if (r.max && (r.state === 'ok' || r.state === 'partial')) {
    const partial = r.state === 'partial' ? ` · ${r.measured} of ${r.claims} volumes measured` : ''
    return {
      text: `${Math.round(r.max.ratio * 100)}% used`,
      tone: cnpgDiskTone(r.max.ratio),
      source: `Fullest: ${cnpgVolumeRoleLabel(r.max.role, r.max.tablespace)} of ${r.max.instance}, ${formatBytes(r.max.usedBytes)} of ${formatBytes(r.max.capacityBytes)} · ${CNPG_DISK_SOURCE}${partial}`,
    }
  }
  switch (r.state) {
    case 'denied':
      return { text: 'No access', tone: 'unknown', source: r.grant ? `Needs ${r.grant}` : r.reason }
    case 'noSeries':
    case 'noPrometheus':
    case 'ok':
    case 'partial':
      return { text: 'No usage metrics', tone: 'unknown', source: r.reason ?? 'Used space needs Prometheus with kubelet volume stats' }
    case 'notRead':
      return { text: 'Not measured', tone: 'unknown', source: r.reason }
    default:
      return { text: 'Unavailable', tone: 'unknown', source: r.reason }
  }
}

const PROBLEM_RANK = { critical: 0, warning: 1, posture: 2 } as const

/**
 * Joins /api/cnpg/disk into the fleet: every row gets its disk fact, and a
 * volume at or past the warning threshold becomes a problem, so the cluster
 * needs attention. Only a measurement raises one, and the endpoint returns
 * measurements only to callers holding the claim and metrics grants.
 */
export function applyCNPGDisk(fleet: CNPGFleet, readings: CNPGDiskReading[] | undefined): CNPGFleet {
  if (!readings) return fleet
  const byKey = new Map(readings.map((r) => [key(r.namespace, r.name), r]))
  const rows = fleet.rows.map((row) => {
    const reading = byKey.get(row.key)
    const next: CNPGFleetRow = { ...row, disk: cnpgDiskFact(reading) }
    const max = reading?.max
    if (!max || max.ratio < CNPG_DISK_WARNING_RATIO || (reading.state !== 'ok' && reading.state !== 'partial')) return next
    const problem: CNPGProblem = {
      id: `disk:${row.key}`,
      severity: max.ratio >= CNPG_DISK_CRITICAL_RATIO ? 'critical' : 'warning',
      category: 'availability',
      title: `The ${cnpgVolumeRoleLabel(max.role, max.tablespace)} of ${max.instance} is ${Math.round(max.ratio * 100)}% full`,
      detail: `${formatBytes(max.usedBytes)} of ${formatBytes(max.capacityBytes)} used, from ${CNPG_DISK_SOURCE}.`,
      subject: { kind: 'Cluster', group: 'postgresql.cnpg.io', namespace: row.namespace, name: row.name },
      source: 'measurement',
    }
    const problems = [...row.problems, problem].sort((a, b) => PROBLEM_RANK[a.severity] - PROBLEM_RANK[b.severity] || a.title.localeCompare(b.title))
    return { ...next, problems, attention: true, categories: new Set([...row.categories, problem.category]) }
  })
  return finishFleet(rows, fleet.incompleteKinds)
}

const CNPG_LAG_UNMEASURED_SOURCE = 'Pod readiness does not show whether a replica is streaming'

/** One cluster's answer from /api/cnpg/fleet-metrics. */
export interface CNPGFleetMetricsReading {
  namespace: string
  name: string
  /** ok | noStandby | noSeries | denied | ambiguous | scopeMismatch | error | notRead */
  lag: {
    state: string
    grant?: string
    reason?: string
    seconds?: number
    pod?: string
    /** The worst standby's lowest recorded lag over `sustainedWindow`; it was already reporting when the window began. */
    sustainedSeconds?: number
    sustainedPod?: string
    sustainedWindow?: string
  }
  /** ok | noSeries | denied | unavailable | error | notRead */
  growth: { state: string; grant?: string; reason?: string; bytesPerHour?: number; claim?: string; instance?: string }
}

export interface CNPGFleetMetricsSources {
  /** prometheus, or none when Radar has no Prometheus (`reason` says why). */
  source: 'prometheus' | 'none'
  reason?: string
  lagSource?: string
  growthSource?: string
}

export function cnpgLagTone(seconds: number): HealthLevel {
  if (seconds >= 30) return 'unhealthy'
  if (seconds >= 5) return 'degraded'
  return 'healthy'
}

function formatLagSeconds(s: number): string {
  if (s === 0) return '0 s'
  if (s < 1) return `${Math.round(s * 1000)} ms`
  if (s < 90) return `${s.toFixed(1)} s`
  if (s < 5400) return `${Math.round(s / 60)} min`
  return `${(s / 3600).toFixed(1)} h`
}

function measuredReplication(base: CNPGFact, reading: CNPGFleetMetricsReading | undefined, src: CNPGFleetMetricsSources): CNPGFact {
  const prefix = base.text.replace(/ · lag unknown$/, '')
  if (src.source === 'none') {
    return { text: `${prefix} · lag unknown (no metrics)`, tone: 'unknown', source: src.reason ?? 'Replication lag needs Prometheus scraping the CNPG exporter' }
  }
  const lag = reading?.lag
  switch (lag?.state) {
    case 'ok':
      if (lag.seconds === undefined) break
      return {
        text: `${prefix} · max lag ${formatLagSeconds(lag.seconds)}`,
        tone: cnpgLagTone(lag.seconds),
        source: `Largest standby replay lag, ${lag.pod ?? 'a standby'} · ${src.lagSource ?? 'Prometheus'}`,
      }
    case 'noStandby':
      return { text: `${prefix} · no standby reporting lag`, tone: 'unknown', source: `${lag.reason ?? 'No instance reports being a standby'} · ${src.lagSource ?? 'Prometheus'}` }
    case 'denied':
      return { text: `${prefix} · lag unknown (no access)`, tone: 'unknown', source: lag.grant ? `Needs ${lag.grant}` : lag.reason }
  }
  return { text: `${prefix} · lag unknown (no metrics)`, tone: 'unknown', source: lag?.reason ?? 'Replication lag needs Prometheus scraping the CNPG exporter' }
}

/** Volume growth of the fastest-growing claim, as a fact. */
export function cnpgDiskGrowthFact(reading: CNPGFleetMetricsReading | undefined, src: CNPGFleetMetricsSources): CNPGFact | undefined {
  const g = reading?.growth
  if (src.source === 'none' || !g || g.state !== 'ok' || g.bytesPerHour === undefined) return undefined
  const perDay = g.bytesPerHour * 24
  const text = Math.abs(perDay) < 1024 ? 'flat over 6 h' : `${perDay > 0 ? '+' : '−'}${formatBytes(Math.abs(perDay))}/day`
  return { text, tone: 'neutral', source: `Fastest-growing: ${g.claim ?? 'a volume'}${g.instance ? ` of ${g.instance}` : ''} · ${src.growthSource ?? 'Prometheus'}` }
}

/**
 * Joins /api/cnpg/fleet-metrics into the fleet: a cluster whose replication
 * fact is only Pod readiness gets its measured standby lag, or says why it has
 * none; the disk growth, when measured, lands on `diskGrowth`. Only lag that
 * stayed high for the whole sustained window raises a problem; a spike is
 * shown, not judged, and growth is never judged.
 */
export function applyCNPGFleetMetrics(fleet: CNPGFleet, readings: CNPGFleetMetricsReading[] | undefined, src: CNPGFleetMetricsSources | undefined): CNPGFleet {
  if (!src) return fleet
  const byKey = new Map((readings ?? []).map((r) => [key(r.namespace, r.name), r]))
  const rows = fleet.rows.map((row) => {
    const reading = byKey.get(row.key)
    const next: CNPGFleetRow = { ...row, diskGrowth: cnpgDiskGrowthFact(reading, src) }
    if (row.replication.source === CNPG_LAG_UNMEASURED_SOURCE) next.replication = measuredReplication(row.replication, reading, src)
    const problem = sustainedLagProblem(row, reading, src)
    if (!problem) return next
    const problems = [...row.problems, problem].sort((a, b) => PROBLEM_RANK[a.severity] - PROBLEM_RANK[b.severity] || a.title.localeCompare(b.title))
    return { ...next, problems, attention: true, categories: new Set([...row.categories, problem.category]) }
  })
  return finishFleet(rows, fleet.incompleteKinds)
}

export const CNPG_SUSTAINED_LAG_WARNING_SECONDS = 30
export const CNPG_SUSTAINED_LAG_CRITICAL_SECONDS = 300

function sustainedLagProblem(row: CNPGFleetRow, reading: CNPGFleetMetricsReading | undefined, src: CNPGFleetMetricsSources): CNPGProblem | undefined {
  const lag = reading?.lag
  const floor = lag?.sustainedSeconds
  if (src.source !== 'prometheus' || lag?.state !== 'ok' || floor === undefined || floor < CNPG_SUSTAINED_LAG_WARNING_SECONDS) return undefined
  const window = lag.sustainedWindow ? formatWindow(lag.sustainedWindow) : 'the last minutes'
  return {
    id: `lag:${row.key}`,
    severity: floor >= CNPG_SUSTAINED_LAG_CRITICAL_SECONDS ? 'critical' : 'warning',
    category: 'availability',
    title: `Every lag sample from ${lag.sustainedPod ?? 'a standby'} over the last ${window} was at least ${formatLagSeconds(floor)}`,
    detail: `The lowest replay lag Prometheus recorded for it in the last ${window} was ${formatLagSeconds(floor)}, and it was already reporting before that window began (${src.lagSource ?? 'Prometheus'}; scrape gaps are not filled in). A failover to that standby would start that far behind the primary.`,
    subject: { kind: 'Cluster', group: 'postgresql.cnpg.io', namespace: row.namespace, name: row.name },
    source: 'measurement',
  }
}

// Go durations as the server sends them ("10m0s") read as "10 min".
function formatWindow(d: string): string {
  const m = /^(?:(\d+)h)?(?:(\d+)m)?(?:0s)?$/.exec(d)
  if (!m) return d
  const minutes = Number(m[1] ?? 0) * 60 + Number(m[2] ?? 0)
  return minutes >= 60 && minutes % 60 === 0 ? `${minutes / 60} h` : `${minutes} min`
}
