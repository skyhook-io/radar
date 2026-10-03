// CloudNativePG workspace model: pure derivations over the /api/cnpg/workspace
// payload. Every fact here is something the cluster actually reports; when it
// does not report something the value is "unknown", never zero or healthy.

import { formatAge, type HealthLevel } from '../resources/resource-utils'
import { worseTone } from '../ui/status-tone'
import type { Fact, ProblemOrigin, WorkspaceProblem } from '../workspace'
import { formatBytes } from '../../utils/format'
import { formatGrant, type Grant } from '../../utils/grant'
import { issueReasonTitle } from '../issues/severity'
import {
  CNPG_BARMAN_PLUGIN_NAME,
  getCNPGClusterBackupConfig,
  getCNPGClusterBarmanPlugin,
  getCNPGClusterImageTag,
  getCNPGClusterIsReplica,
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

export type CNPGCoverageState = 'full' | 'partial' | 'denied' | 'notInstalled' | 'syncing' | 'uncached' | 'error'

export interface CNPGKindCoverage {
  state: CNPGCoverageState
  /** Denied namespaces, named only when the caller supplied the candidate list. */
  deniedNamespaces?: string[]
  /** Namespaces the caller may read but Radar's cache does not hold, named under the same rule. */
  uncachedNamespaces?: string[]
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
  /** Each ScheduledBackup's schedule as the operator reads it, keyed "namespace/name"; absent when it cannot be parsed. */
  scheduleReadings?: Record<string, string>
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

export type CNPGProblemCategory = 'availability' | 'protection' | 'declarations' | 'pooling'

export const CNPG_PROBLEM_CATEGORIES: { id: CNPGProblemCategory; label: string }[] = [
  { id: 'availability', label: 'Availability' },
  { id: 'protection', label: 'Protection' },
  { id: 'declarations', label: 'Declarations' },
  { id: 'pooling', label: 'Pooling' },
]

/** A problem in the CloudNativePG workspace, categorised by the workspace's four screens. */
export type CNPGProblem = WorkspaceProblem<CNPGProblemCategory>

export interface CNPGInstance {
  name: string
  role: 'primary' | 'replica' | 'unknown'
  ready: boolean | null
  node?: string
  zone?: string
}

export interface CNPGProtectionFacts {
  schedule: Fact & { names: string[] }
  destination: Fact & {
    method: 'plugin' | 'barmanObjectStore' | 'volumeSnapshot' | 'none'
    objectStore?: string
  }
  lastSuccessfulBackup: Fact
  walArchiving: Fact
  recoveryWindow: Fact & { from?: string }
  restoreValidation: Fact & { restoredInto?: { namespace: string; name: string } }
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
  replication: Fact
  protection: CNPGProtectionFacts & { summary: Fact }
  declarations: { summary: Fact; total: number; failed: number; pending: number }
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
  disk?: Fact
  /** Growth of the fastest-growing volume, set by applyCNPGFleetMetrics when measured. */
  diskGrowth?: Fact
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

// What an instance Pod's bare reason means, said about the Pod.
const CNPG_POD_REASON_SENTENCES: Record<string, string> = {
  ReadinessProbeFailed: 'not ready (readiness probe failing)',
  // The issue does not say whether the Pod is serving now (it may have come
  // back within the settle window), so restarts are worded as past.
  LivenessProbeFailed: 'restarted recently (liveness probe failing)',
  CrashLoopBackOff: 'restarted recently (CrashLoopBackOff)',
  HighRestartCount: 'restarted repeatedly',
  OOMKilled: 'killed for running out of memory (OOMKilled)',
  ImagePullBackOff: 'cannot pull its image (ImagePullBackOff)',
  ErrImagePull: 'cannot pull its image (ErrImagePull)',
}

// Plain headlines for CNPG issues whose message carries the operator's own
// condition text; that message becomes the detail beneath.
// Reasons the Issues page already titles come from issueReasonTitle, so a
// problem reads the same here and there; these are the rest.
const CNPG_REASON_TITLES: Record<string, string> = {
  CNPGBackupFailed: 'Backup failed',
  CNPGScheduledBackupMissed: 'A scheduled backup did not run',
  CNPGCertificateExpiring: 'A certificate expires soon',
  CNPGCertificateExpired: 'A certificate has expired',
}

/**
 * A problem's headline and detail from an issue. Known CNPG reasons get a
 * short plain title with the operator's message beneath; otherwise the
 * message is the title, unless it is empty or only the reason token (e.g.
 * "ReadinessProbeFailed"), which is turned into a sentence about the subject.
 */
export function cnpgIssueText(issue: Pick<CNPGWorkspaceIssue, 'kind' | 'name' | 'reason' | 'message' | 'cause' | 'first_seen'>): { title: string; detail?: string } {
  let message = issue.message?.trim() ?? ''
  const cause = issue.cause?.trim() || undefined
  // The run's time is the issue's first_seen, not part of the message.
  if (issue.reason === 'CNPGScheduledRunNoBackup' && issue.first_seen && message) message = `${message} ${formatAge(issue.first_seen)} ago`
  const known = issueReasonTitle(issue.reason) ?? CNPG_REASON_TITLES[issue.reason]
  if (known) return { title: known, detail: [stripTitlePrefix(message, known), cause].filter(Boolean).join(' ') || undefined }
  if (message && message !== issue.reason && /\s/.test(message)) return { title: message, detail: cause }
  const token = message || issue.reason
  const sentence = CNPG_POD_REASON_SENTENCES[token]
  if (sentence) return { title: `${issue.name} ${sentence}`, detail: cause }
  const words = token.replace(/([a-z])([A-Z])/g, '$1 $2').toLowerCase()
  return { title: `${issue.kind} ${issue.name}: ${words}`, detail: cause }
}

// "Backup failed: cannot proceed…" under the title "Backup failed" repeats it.
function stripTitlePrefix(message: string, title: string): string {
  if (!message.toLowerCase().startsWith(title.toLowerCase())) return message
  const rest = message.slice(title.length).replace(/^[\s:;,.\-–—]+/, '')
  return rest ? rest[0].toUpperCase() + rest.slice(1) : ''
}

/**
 * Failed Backups of one cluster that failed for the same reason become one
 * problem: "3 backups failed: <reason>", about the latest of them, the others
 * named in alsoAbout. Each Backup is otherwise its own issue, and a schedule
 * failing every night would list the same sentence over and over.
 */
type IssueProblem = CNPGProblem & { reason?: string }

export function cnpgCollapseBackupFailures(problems: IssueProblem[], backupTimes: Map<string, number> = new Map()): IssueProblem[] {
  const groups = new Map<string, IssueProblem[]>()
  const out: IssueProblem[] = []
  for (const p of problems) {
    if (p.reason !== 'CNPGBackupFailed' || p.subject.kind !== 'Backup') {
      out.push(p)
      continue
    }
    const key = `${p.severity}\x00${p.detail ?? ''}`
    groups.set(key, [...(groups.get(key) ?? []), p])
  }
  for (const list of groups.values()) {
    if (list.length === 1) {
      out.push(list[0])
      continue
    }
    const at = (p: IssueProblem) => backupTimes.get(p.subject.name) ?? 0
    const sorted = [...list].sort((a, b) => at(b) - at(a) || b.subject.name.localeCompare(a.subject.name))
    const latest = sorted[0]
    out.push({
      ...latest,
      id: `backups-failed:${latest.subject.namespace}:${latest.detail ?? ''}`,
      title: latest.detail ? `${list.length} backups failed: ${latest.detail[0].toLowerCase()}${latest.detail.slice(1)}` : `${list.length} backups failed`,
      detail: undefined,
      alsoAbout: sorted.slice(1).map((p) => ({ kind: p.subject.kind, name: p.subject.name })),
    })
  }
  return out
}

/**
 * "Latest backup failed" restates a failed-Backup problem when that problem
 * is about the cluster's newest Backup, so the duplicate is dropped and the
 * count stays honest. With the newest Backup unknown, both stay.
 */
export function cnpgFoldLastBackupFailed(problems: IssueProblem[], newestBackup: string | undefined): CNPGProblem[] {
  const covered =
    !!newestBackup &&
    problems.some(
      (p) =>
        p.reason === 'CNPGBackupFailed' &&
        p.subject.kind === 'Backup' &&
        (p.subject.name === newestBackup || p.alsoAbout?.some((o) => o.kind === 'Backup' && o.name === newestBackup)),
    )
  return problems
    .filter((p) => !(covered && p.reason === 'CNPGLastBackupFailed'))
    .map(({ reason: _r, ...p }) => p)
}

// When each of the cluster's Backups started (status.startedAt, else its
// creation), by name: the issues about Backups carry no time of their own.
function backupTimesOf(cluster: any, backups: any[]): Map<string, number> {
  const ns = cluster.metadata?.namespace
  const name = cluster.metadata?.name
  const out = new Map<string, number>()
  for (const b of backups) {
    if (b?.metadata?.namespace !== ns || specClusterName(b) !== name) continue
    out.set(b.metadata.name, Date.parse(b?.status?.startedAt ?? b?.metadata?.creationTimestamp ?? '') || 0)
  }
  return out
}


// Each entry names what the Go detector (internal/issues/source_cnpg*.go and
// the Pod detector) actually reads. "Reported by CNPG" only where the operator
// itself wrote the failure; a threshold or comparison Radar applies is a
// "Radar check".
const CNPG_CONDITION_ORIGINS: Record<string, string> = {
  CNPGLastBackupFailed: 'Cluster LastBackupSucceeded condition',
  CNPGClusterTerminal: 'Cluster status.phase',
  CNPGClusterUnrecoverable: 'Cluster status.phase',
  CNPGClusterPluginFailure: 'Cluster status.phase',
  CNPGClusterFailingOver: 'Cluster status.phase',
  CNPGClusterWaitingForUser: 'Cluster status.phase',
  CNPGDeclarativeNotApplied: 'status.applied and status.message',
}

const POD_ORIGINS: Record<string, ProblemOrigin> = {
  ReadinessProbeFailed: { label: 'Pod readiness probe', detail: 'Kubelet probe-failure events and the Pod\'s Ready condition' },
  LivenessProbeFailed: { label: 'Pod liveness probe', detail: 'Kubelet probe-failure events and container restarts' },
  ReadinessProbeInvalid: { label: 'Radar check of the probe', detail: 'The readiness probe names a port the container does not declare' },
  LivenessProbeInvalid: { label: 'Radar check of the probe', detail: 'The liveness probe names a port the container does not declare' },
  HighRestartCount: { label: 'Radar check of restarts', detail: 'More than 3 restarts on a container that is still unhealthy' },
  InitContainerStalled: { label: 'Radar check of init containers', detail: 'An init container has not finished' },
}

/**
 * Where an issue's evidence comes from, in user terms: what CloudNativePG
 * reported, a Backup's or Pod's own status, or Radar's own check. A reason this
 * does not know reads "Detected by Radar" rather than a guessed source.
 */
export function cnpgIssueOrigin(issue: Pick<CNPGWorkspaceIssue, 'kind' | 'reason'>): ProblemOrigin {
  const condition = CNPG_CONDITION_ORIGINS[issue.reason]
  if (condition) return { label: 'Reported by CNPG', detail: condition }
  switch (issue.reason) {
    case 'CNPGWALArchivingFailing':
      return issue.kind === 'Backup'
        ? { label: 'Backup status', detail: 'Backup status.phase walArchivingFailing and status.error' }
        : { label: 'Reported by CNPG', detail: 'Cluster ContinuousArchiving condition' }
    case 'CNPGBackupFailed':
      return { label: 'Backup status', detail: 'Backup status.phase and status.error' }
    case 'CNPGClusterDegraded':
      return { label: 'Radar check of ready instances', detail: 'spec.instances against status.readyInstances, unless the phase, hibernation or fencing explains it' }
    case 'CNPGScheduledRunNoBackup':
      return { label: 'Radar check of the backup schedule', detail: 'The schedule, read as the operator does, against the cluster\'s newest successful backup' }
    case 'CNPGScheduledBackupMissed':
      return { label: 'Radar check of the backup schedule', detail: 'ScheduledBackup status.nextScheduleTime passed more than 10 minutes ago' }
    case 'CNPGCertificateExpiring':
    case 'CNPGCertificateExpired':
      return { label: 'Certificate expiry (from Cluster status)', detail: 'Cluster status.certificates.expirations, compared with now' }
  }
  if (issue.kind === 'Pod') return POD_ORIGINS[issue.reason] ?? { label: 'Pod status' }
  return { label: 'Detected by Radar' }
}

/** The headline alone; see cnpgIssueText. */
export function cnpgIssueTitle(issue: Pick<CNPGWorkspaceIssue, 'kind' | 'name' | 'reason' | 'message'>): string {
  return cnpgIssueText(issue).title
}

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
    if (cov.deniedNamespaces || cov.uncachedNamespaces) return !cov.deniedNamespaces?.includes(namespace) && !cov.uncachedNamespaces?.includes(namespace)
    return false
  }
  return false
}

/**
 * Why a kind's objects were not read (in `namespace`, when given), worded for
 * a fact. A namespace Radar's cache does not hold is never called "no access";
 * a partial read that names neither cause says only that it was not read.
 */
export function cnpgCoverageGap(cov: CNPGKindCoverage, what: string, namespace?: string, notInstalled = 'Not installed'): string {
  switch (cov.state) {
    case 'denied':
      return `No access to ${what}`
    case 'partial':
      if (namespace && cov.uncachedNamespaces?.includes(namespace)) return `Radar does not cache ${what} in ${namespace}`
      if (namespace && cov.deniedNamespaces?.includes(namespace)) return `No access to ${what}`
      return namespace ? `${what} not read in ${namespace}` : `${what} not read`
    case 'uncached':
      return `Radar does not cache ${what}`
    case 'syncing':
      return 'Loading…'
    case 'error':
      return `Could not read ${what}`
    default:
      return notInstalled
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
  readings: Record<string, string> = {},
): CNPGProtectionFacts['schedule'] {
  const ns = cluster.metadata?.namespace
  if (!coverageReadable(cov, ns)) {
    return { text: cnpgCoverageGap(cov, 'ScheduledBackups', ns), tone: 'unknown', names: [] }
  }
  const mine = schedules.filter((s) => s.metadata?.namespace === ns && specClusterName(s) === cluster.metadata?.name)
  if (mine.length === 0) return { text: 'No declarative schedule', tone: 'neutral', names: [] }
  const active = mine.filter((s) => s.spec?.suspend !== true)
  const names = mine.map((s) => s.metadata?.name).filter(Boolean)
  if (active.length === 0) {
    return { text: mine.length === 1 ? 'Schedule suspended' : 'All schedules suspended', tone: 'degraded', names }
  }
  const cron = active[0]?.spec?.schedule
  const reading = readings[`${ns}/${active[0]?.metadata?.name}`]
  return {
    text: active.length === 1 ? (reading ? `Scheduled · ${reading}` : cron ? `Scheduled · ${cron}` : 'Scheduled') : `${active.length} schedules`,
    tone: 'healthy',
    names,
    ...(active.length === 1 && cron ? { source: `ScheduledBackup ${active[0]?.metadata?.name} · cron ${cron}` } : {}),
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

function recoveryWindowFor(cluster: any, stores: any[]): { from?: string; lastSuccess?: string; store?: string } | null {
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
      return { text: cnpgCoverageGap(backupsCov, 'Backups', ns), tone: 'unknown' }
    }
    if (storesUnreadable) return { text: cnpgCoverageGap(storesUnreadable, 'ObjectStores', ns), tone: 'unknown' }
    return { text: 'None observed', tone: 'unknown' }
  }
  const best = candidates.reduce((a, b) => (Date.parse(a.at) >= Date.parse(b.at) ? a : b))
  return { text: 'Completed', tone: 'healthy', at: best.at, source: best.source }
}

const ARCHIVING_RECENT_MS = 24 * 3_600_000
const ARCHIVING_SETTLE_MS = 10 * 60_000

function walFact(cluster: any, now = Date.now()): Fact {
  const conds = cluster?.status?.conditions
  const c = Array.isArray(conds) ? conds.find((x: any) => x?.type === 'ContinuousArchiving') : null
  if (!c) return { text: 'Not reported', tone: 'unknown', source: 'Cluster status' }
  if (c.status === 'True') {
    // A recent change well after creation shows when archiving started working,
    // e.g. after a fix; it says nothing about what came before it.
    const since = Date.parse(c.lastTransitionTime ?? '')
    const created = Date.parse(cluster?.metadata?.creationTimestamp ?? '')
    if (now - since < ARCHIVING_RECENT_MS && since - created > ARCHIVING_SETTLE_MS) {
      return { text: 'Archiving', tone: 'healthy', source: 'ContinuousArchiving condition, True since then', at: c.lastTransitionTime, atMeaning: 'since' }
    }
    return { text: 'Archiving', tone: 'healthy', source: 'ContinuousArchiving condition' }
  }
  if (c.status === 'False') {
    return {
      text: 'Failing',
      tone: 'unhealthy',
      source: 'ContinuousArchiving condition',
      ...(c.lastTransitionTime ? { at: c.lastTransitionTime, atMeaning: 'since' as const } : {}),
      ...(c.message ? { detail: c.message } : {}),
    }
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
  backupsReadable: boolean,
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
  if (!restored) {
    // A recovery by Backup name is only attributable when that Backup could be read.
    const unresolved = !backupsReadable && allClusters.some((c) => c !== cluster && c.metadata?.namespace === ns && c.spec?.bootstrap?.recovery?.backup?.name)
    if (unresolved) return { text: 'Unknown: no access to Backups', tone: 'unknown', source: `A Cluster in ${ns} recovers from a Backup Radar cannot read` }
    return { text: 'None recorded', tone: 'unknown', source: 'Kubernetes does not record restore tests' }
  }
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

function protectionSummary(p: CNPGProtectionFacts): Fact {
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

function replicationFact(cluster: any, pods: CNPGInstance[], hibernated: boolean, podsCov: CNPGKindCoverage): Fact {
  if (hibernated) return { text: 'Hibernated', tone: 'neutral' }
  const desired = cluster?.spec?.instances
  if (desired === 1) return { text: 'Single instance', tone: 'neutral' }
  if (!coverageReadable(podsCov, cluster?.metadata?.namespace)) {
    return { text: cnpgCoverageGap(podsCov, 'Pods', cluster?.metadata?.namespace), tone: 'unknown' }
  }
  const replicas = pods.filter((p) => p.role === 'replica')
  const readyReplicas = replicas.filter((p) => p.ready === true).length
  if (replicas.length === 0) return { text: 'No replica pods observed', tone: 'unknown' }
  return {
    text: `${readyReplicas}/${replicas.length} Pods ready · lag unknown`,
    tone: 'unknown',
    source: CNPG_LAG_UNMEASURED_SOURCE,
  }
}

function problemsFor(
  cluster: any,
  issues: CNPGWorkspaceIssue[],
  audit: CNPGAuditFinding[],
  children: Map<string, string>,
  backupTimes: Map<string, number> = new Map(),
): CNPGProblem[] {
  const ns = cluster.metadata?.namespace
  const name = cluster.metadata?.name
  const out: CNPGProblem[] = []
  const fromIssues: IssueProblem[] = []
  for (const issue of issues) {
    if ((issue.namespace ?? '') !== ns) continue
    const isSelf = issue.kind === 'Cluster' && issue.name === name
    const owner = children.get(`${issue.kind}/${ns}/${issue.name}`)
    if (!isSelf && owner !== name) continue
    fromIssues.push({
      id: `${issue.id}:${issue.kind}/${issue.name}`,
      severity: issue.severity,
      category: cnpgIssueCategory(issue),
      ...cnpgIssueText(issue),
      subject: { kind: issue.kind, group: issue.group ?? '', namespace: ns, name: issue.name },
      source: 'issue',
      origin: cnpgIssueOrigin(issue),
      reason: issue.reason,
    })
  }
  const newestBackup = [...backupTimes.entries()].sort((a, b) => b[1] - a[1])[0]?.[0]
  out.push(...cnpgFoldLastBackupFailed(cnpgCollapseBackupFailures(fromIssues, backupTimes), newestBackup))
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
  return out.sort(cnpgCompareProblems)
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

const CNPG_SEVERITY_RANK = { critical: 0, warning: 1, posture: 2 } as const

/**
 * The order problems are shown in, everywhere: most severe first, then a
 * cause before its symptoms. One instance Pod's state (a failing probe, a
 * crash loop) is usually the symptom of a cluster-level problem (archiving,
 * backups, replication, reconciliation, declarations), so at equal severity
 * those come first.
 */
export function cnpgCompareProblems(a: CNPGProblem, b: CNPGProblem): number {
  const symptom = (p: CNPGProblem) => (p.subject.kind === 'Pod' && p.subject.group === '' ? 1 : 0)
  return CNPG_SEVERITY_RANK[a.severity] - CNPG_SEVERITY_RANK[b.severity] || symptom(a) - symptom(b) || a.title.localeCompare(b.title)
}

function sortProblems(list: CNPGProblem[]): CNPGProblem[] {
  return list.sort(cnpgCompareProblems)
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
  let summary: Fact
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
    const wal = walFact(cluster)
    const storesCov = coverageOf(resp, 'objectStores')
    const storesUnreadable = !!getCNPGClusterBarmanPlugin(cluster)?.barmanObjectName && !coverageReadable(storesCov, ns)
    const protection: CNPGProtectionFacts = {
      schedule: scheduleFact(cluster, resp.objects.scheduledBackups ?? [], coverageOf(resp, 'scheduledBackups'), resp.scheduleReadings),
      destination: destinationFact(cluster),
      lastSuccessfulBackup: lastBackupFact(cluster, resp.objects.backups ?? [], coverageOf(resp, 'backups'), window, storesUnreadable ? storesCov : null),
      walArchiving: wal,
      // The latest recoverable point follows WAL archiving, not the last base
      // backup, and no status reports it; only failing archiving stops it.
      recoveryWindow: window?.from
        ? {
            text: 'Recoverable window',
            tone: wal.tone === 'unhealthy' ? 'degraded' : 'neutral',
            from: window.from,
            source: `ObjectStore ${window.store} status (earliest point)`,
          }
        : storesUnreadable
          ? { text: cnpgCoverageGap(storesCov, 'ObjectStores', ns), tone: 'unknown' }
          : { text: 'Not reported', tone: 'unknown' },
      restoreValidation: restoreValidationFact(cluster, clusters, resp.objects.backups ?? [], coverageReadable(coverageOf(resp, 'backups'), ns)),
    }
    const podsReadable = coverageReadable(coverageOf(resp, 'pods'), ns)
    const podReadiness =
      podsReadable && instancePods.length > 0 && instancePods.every((p) => p.ready !== null)
        ? { ready: instancePods.filter((p) => p.ready).length, total: instancePods.length }
        : undefined
    const readinessContradicted = !hibernated && !!podReadiness && readyInstances !== null && podReadiness.ready < readyInstances
    const primaryConflict = podsReadable ? primaryConflictOf(cluster, pods.filter((p) => p.metadata?.namespace === ns && p.metadata?.labels?.['cnpg.io/cluster'] === name)) : undefined
    const problems = sortProblems([
      ...problemsFor(cluster, resp.issues ?? [], resp.audit ?? [], children, backupTimesOf(cluster, resp.objects.backups ?? [])),
      ...observedProblems(cluster, instancePods, readinessContradicted ? podReadiness : undefined, readyInstances, primaryConflict),
    ])
    const categories = new Set<CNPGProblemCategory>(
      problems.filter((p) => p.severity !== 'posture').map((p) => p.category),
    )
    const replica = cluster && getCNPGClusterIsReplica(cluster) ? { source: cluster.spec.replica.source } : null

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
    worst = Math.min(worst, CNPG_SEVERITY_RANK[p.severity])
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
  grant?: Grant
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
  isolation?: CNPGMetricIsolation
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
export function cnpgDiskFact(r: CNPGDiskReading | undefined): Fact {
  if (!r) return { text: 'Not read', tone: 'unknown' }
  if (r.max && (r.state === 'ok' || r.state === 'partial')) {
    const partial = r.state === 'partial' ? ` · ${r.measured} of ${r.claims} volumes measured` : ''
    return {
      text: `${Math.round(r.max.ratio * 100)}% used`,
      tone: cnpgDiskTone(r.max.ratio),
      source: `Fullest: ${cnpgVolumeRoleLabel(r.max.role, r.max.tablespace)} of ${r.max.instance}, ${formatBytes(r.max.usedBytes)} of ${formatBytes(r.max.capacityBytes)} · ${CNPG_DISK_SOURCE}${partial}.${isolationCaveat(r.isolation)}`,
    }
  }
  switch (r.state) {
    case 'denied':
      return { text: 'No access', tone: 'unknown', source: r.grant ? `Needs ${formatGrant(r.grant)}` : r.reason }
    case 'noPrometheus':
      return { text: 'No usage metrics', tone: 'unknown', source: CNPG_PROMETHEUS_NOT_CONNECTED, detail: r.reason }
    case 'noSeries':
    case 'ok':
    case 'partial':
      return { text: 'No usage metrics', tone: 'unknown', source: r.reason ?? 'Used space needs Prometheus with kubelet volume stats' }
    case 'notRead':
      return { text: 'Not measured', tone: 'unknown', source: r.reason }
    default:
      return { text: 'Unavailable', tone: 'unknown', source: r.reason }
  }
}

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
      detail: `${formatBytes(max.usedBytes)} of ${formatBytes(max.capacityBytes)} used, from ${CNPG_DISK_SOURCE}.${isolationCaveat(reading.isolation)}`,
      subject: { kind: 'Cluster', group: 'postgresql.cnpg.io', namespace: row.namespace, name: row.name },
      source: 'measurement',
      ...(reading.isolation?.mode === 'unverified' ? { measuredBy: 'kubelet, matched by claim name', unverifiedMatch: true } : {}),
    }
    const problems = [...row.problems, problem].sort(cnpgCompareProblems)
    return { ...next, problems, attention: true, categories: new Set([...row.categories, problem.category]) }
  })
  return finishFleet(rows, fleet.incompleteKinds)
}

/** The short per-fact source when Radar has no Prometheus; the reason goes in `detail`. */
export const CNPG_PROMETHEUS_NOT_CONNECTED = 'Prometheus not connected'

const CNPG_LAG_UNMEASURED_SOURCE = 'Pod readiness does not show whether a replica is streaming'

/** One cluster's answer from /api/cnpg/fleet-metrics. */
export interface CNPGFleetMetricsReading {
  namespace: string
  name: string
  /** ok | noStandby | noSeries | denied | ambiguous | scopeMismatch | error | notRead */
  lag: {
    state: string
    grant?: Grant
    reason?: string
    seconds?: number
    pod?: string
    /** The worst standby's lowest recorded lag over `sustainedWindow`, across every scrape of it; it was already reporting by the window's start. */
    sustainedSeconds?: number
    sustainedPod?: string
    sustainedWindow?: string
    isolation?: CNPGMetricIsolation
  }
  /** ok | noSeries | denied | unavailable | error | notRead */
  growth: { state: string; grant?: Grant; reason?: string; bytesPerHour?: number; claim?: string; instance?: string; isolation?: CNPGMetricIsolation }
}

/** How Prometheus series were tied to one cluster; `unverified` matched only by namespace and Pod or claim names. */
export interface CNPGMetricIsolation {
  mode: 'configured' | 'verified' | 'unverified'
  note: string
}

// A finding stated as this cluster's must say when its series were matched by name alone.
function isolationCaveat(iso: CNPGMetricIsolation | undefined): string {
  return iso?.mode === 'unverified' ? ` ${iso.note}.` : ''
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

/**
 * Replication's tone from the primary's pg_stat_replication: a missing
 * standby is degraded, and the lag of the ones that do stream can make it
 * worse. Missing standbys never hide a severe lag.
 */
export function cnpgReplicationTone(streaming: number, expected: number, maxLagSeconds: number | undefined): HealthLevel {
  const missing: HealthLevel = streaming < expected ? 'degraded' : 'healthy'
  return maxLagSeconds === undefined ? missing : worseTone(missing, cnpgLagTone(maxLagSeconds))
}

/**
 * The one way a replay lag reads: milliseconds below a second, one decimal
 * below 10 s, whole seconds below 100 s, then whole minutes, then hours and
 * minutes. Rounded down, so a lower bound stays one.
 */
export function cnpgFormatLag(s: number): string {
  if (s <= 0) return '0 s'
  if (s < 1) return `${Math.floor(s * 1000)} ms`
  if (s < 10) return `${(Math.floor(s * 10) / 10).toFixed(1)} s`
  if (s < 100) return `${Math.floor(s)} s`
  const minutes = Math.floor(s / 60)
  if (minutes < 60) return `${minutes} min`
  const m = minutes % 60
  return m === 0 ? `${Math.floor(minutes / 60)} h` : `${Math.floor(minutes / 60)} h ${m} min`
}

function measuredReplication(base: Fact, reading: CNPGFleetMetricsReading | undefined, src: CNPGFleetMetricsSources): Fact {
  const prefix = base.text.replace(/ · lag unknown$/, '')
  if (src.source === 'none') {
    return { text: `${prefix} · lag unknown`, tone: 'unknown', source: CNPG_PROMETHEUS_NOT_CONNECTED, detail: src.reason }
  }
  const lag = reading?.lag
  switch (lag?.state) {
    case 'ok':
      if (lag.seconds === undefined) break
      return {
        text: `${prefix} · lag ${cnpgFormatLag(lag.seconds)}`,
        tone: cnpgLagTone(lag.seconds),
        source: `Largest standby replay lag, ${lag.pod ?? 'a standby'} · ${src.lagSource ?? 'Prometheus'}.${isolationCaveat(lag.isolation)}`,
      }
    case 'noStandby':
      return { text: `${prefix} · lag unknown`, tone: 'unknown', source: `No standby reports lag: ${lag.reason ?? 'no instance reports being a standby'} · ${src.lagSource ?? 'Prometheus'}` }
    case 'denied':
      return { text: `${prefix} · lag unknown`, tone: 'unknown', source: lag.grant ? `Needs ${formatGrant(lag.grant)}` : lag.reason }
  }
  return { text: `${prefix} · lag unknown`, tone: 'unknown', source: lag?.reason ?? 'Replication lag needs Prometheus scraping the CNPG exporter' }
}

/** Volume growth of the fastest-growing claim, as a fact. */
export function cnpgDiskGrowthFact(reading: CNPGFleetMetricsReading | undefined, src: CNPGFleetMetricsSources): Fact | undefined {
  const g = reading?.growth
  if (src.source === 'none' || !g || g.state !== 'ok' || g.bytesPerHour === undefined) return undefined
  const perDay = g.bytesPerHour * 24
  const text = Math.abs(perDay) < 1024 ? 'flat over 6 h' : `${perDay > 0 ? '+' : '−'}${formatBytes(Math.abs(perDay))}/day`
  return { text, tone: 'neutral', source: `Fastest-growing: ${g.claim ?? 'a volume'}${g.instance ? ` of ${g.instance}` : ''} · ${src.growthSource ?? 'Prometheus'}.${isolationCaveat(g.isolation)}` }
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
    const problems = [...row.problems, problem].sort(cnpgCompareProblems)
    return { ...next, problems, attention: true, categories: new Set([...row.categories, problem.category]) }
  })
  return finishFleet(rows, fleet.incompleteKinds)
}

export const CNPG_SUSTAINED_LAG_WARNING_SECONDS = 30
export const CNPG_SUSTAINED_LAG_CRITICAL_SECONDS = 300

/** The id of a cluster's sustained-lag problem, so other views can find it among the row's problems. */
export function cnpgSustainedLagProblemId(rowKey: string): string {
  return `lag:${rowKey}`
}

function sustainedLagProblem(row: CNPGFleetRow, reading: CNPGFleetMetricsReading | undefined, src: CNPGFleetMetricsSources): CNPGProblem | undefined {
  const lag = reading?.lag
  const floor = lag?.sustainedSeconds
  if (src.source !== 'prometheus' || lag?.state !== 'ok' || floor === undefined || floor < CNPG_SUSTAINED_LAG_WARNING_SECONDS) return undefined
  const window = lag.sustainedWindow ? formatWindowWords(lag.sustainedWindow) : 'several minutes'
  const pod = lag.sustainedPod ?? 'A standby'
  // The query proves every recorded sample was at least the floor and that
  // the series existed at the window's start, not that samples were continuous.
  return {
    id: cnpgSustainedLagProblemId(row.key),
    severity: floor >= CNPG_SUSTAINED_LAG_CRITICAL_SECONDS ? 'critical' : 'warning',
    category: 'availability',
    title: `${pod} ≥ ${cnpgFormatLag(floor)} behind in every sample for ${formatWindowShort(lag.sustainedWindow)}`,
    shortTitle: `${pod}: all samples ≥ ${cnpgFormatLag(floor)} behind (${formatWindowShort(lag.sustainedWindow)})`,
    detail: `Lowest replay lag in the samples Prometheus recorded over the last ${window}. If Prometheus missed some scrapes, those moments aren't included. If it was still that far behind, a failover to it would lose or wait on that much WAL.${isolationCaveat(lag.isolation)}`,
    subject: { kind: 'Cluster', group: 'postgresql.cnpg.io', namespace: row.namespace, name: row.name },
    source: 'measurement',
    measuredBy: lag.isolation?.mode === 'unverified' ? 'Prometheus, matched by Pod name' : 'Prometheus',
    unverifiedMatch: lag.isolation?.mode === 'unverified',
    sourceDetail: src.lagSource ?? 'Prometheus',
  }
}

// "10m0s" as "10 min"; "1h0m0s" as "1 h", for a title that must stay short.
function formatWindowShort(d: string | undefined): string {
  const m = d ? /^(?:(\d+)h)?(?:(\d+)m)?(?:0s)?$/.exec(d) : null
  if (!m) return d ?? 'minutes'
  const minutes = Number(m[1] ?? 0) * 60 + Number(m[2] ?? 0)
  return minutes >= 60 && minutes % 60 === 0 ? `${minutes / 60} h` : `${minutes} min`
}

// "10m0s" as "10 minutes"; "1h0m0s" as "1 hour".
function formatWindowWords(d: string): string {
  const m = /^(?:(\d+)h)?(?:(\d+)m)?(?:0s)?$/.exec(d)
  if (!m) return d
  const minutes = Number(m[1] ?? 0) * 60 + Number(m[2] ?? 0)
  if (minutes >= 60 && minutes % 60 === 0) return minutes === 60 ? '1 hour' : `${minutes / 60} hours`
  return minutes === 1 ? '1 minute' : `${minutes} minutes`
}

