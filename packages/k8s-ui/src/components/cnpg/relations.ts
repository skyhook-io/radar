// Pure relationship lookups between CloudNativePG objects in the workspace
// payload. Each helper answers only from what the objects record; a relation
// that cannot be established returns null or an empty list, never a guess.

import type { BadgeSeverity } from '../ui/Badge'
import type { HealthLevel } from '../resources/resource-utils'
import {
  CNPG_BARMAN_PLUGIN_NAME,
  CNPG_GROUP,
  getCNPGClusterBarmanPlugin,
  getCNPGObjectStoreRecoveryWindows,
  isApiGroup,
  type CNPGObjectStoreRecoveryWindow,
} from '../resources/resource-utils-cnpg'
import {
  cnpgIssueCategory,
  coverageReadable,
  type CNPGFact,
  type CNPGProblem,
  type CNPGWorkspaceIssue,
  type CNPGWorkspaceKey,
  type CNPGWorkspaceResponse,
} from './workspace'

export interface CNPGObjectRef {
  kind: string
  group: string
  namespace: string
  name: string
}

const SCHEDULED_BACKUP_LABEL = 'cnpg.io/scheduled-backup'

function nsOf(obj: any): string {
  return obj?.metadata?.namespace ?? ''
}

function nameOf(obj: any): string {
  return obj?.metadata?.name ?? ''
}

function specCluster(obj: any): string | undefined {
  const n = obj?.spec?.cluster?.name
  return typeof n === 'string' && n ? n : undefined
}

function parseTime(t: unknown): number {
  const ms = typeof t === 'string' ? Date.parse(t) : NaN
  return Number.isFinite(ms) ? ms : 0
}

/** Kind and API group both match. Kind alone is not identity: Velero also ships `Backup`. */
export function isCNPGKind(obj: any, kind: string, group: string = CNPG_GROUP): boolean {
  return obj?.kind === kind && isApiGroup(obj?.apiVersion, group)
}

export function refOf(obj: any, kind: string, group: string = CNPG_GROUP): CNPGObjectRef {
  return { kind, group, namespace: nsOf(obj), name: nameOf(obj) }
}

export function healthSeverity(level: HealthLevel): BadgeSeverity {
  switch (level) {
    case 'healthy':
      return 'success'
    case 'unhealthy':
      return 'error'
    case 'alert':
      return 'alert'
    case 'degraded':
      return 'warning'
    default:
      return 'neutral'
  }
}

// ---------------------------------------------------------------------------
// Workspace access
// ---------------------------------------------------------------------------

export function workspaceList(ws: CNPGWorkspaceResponse | null | undefined, key: CNPGWorkspaceKey): any[] {
  return ws?.objects?.[key] ?? []
}

/**
 * Why the workspace cannot answer for `key` in `namespace`, or null when it can.
 * A null workspace means the aggregate could not be read at all.
 */
export function relationUnavailable(
  ws: CNPGWorkspaceResponse | null | undefined,
  key: CNPGWorkspaceKey,
  namespace: string | undefined,
  what: string,
): string | null {
  if (!ws) return `${what} could not be read`
  const cov = ws.coverage?.[key] ?? { state: 'notInstalled' as const }
  if (coverageReadable(cov, namespace)) return null
  switch (cov.state) {
    case 'denied':
    case 'partial':
      return `No access to ${what}`
    case 'syncing':
      return 'Loading…'
    case 'error':
      return `Could not read ${what}`
    default:
      return `${what} are not installed`
  }
}

export function clustersIn(ws: CNPGWorkspaceResponse | null | undefined): any[] {
  return workspaceList(ws, 'clusters').filter((c) => isCNPGKind(c, 'Cluster'))
}

/** The Cluster an object declares itself against (`spec.cluster.name`), if visible. */
export function targetCluster(obj: any, clusters: any[]): any | null {
  const name = specCluster(obj)
  if (!name) return null
  return clusters.find((c) => isCNPGKind(c, 'Cluster') && nsOf(c) === nsOf(obj) && nameOf(c) === name) ?? null
}

// ---------------------------------------------------------------------------
// Issues
// ---------------------------------------------------------------------------

export function issuesForObject(issues: CNPGWorkspaceIssue[] | undefined, ref: CNPGObjectRef): CNPGWorkspaceIssue[] {
  const rank = { critical: 0, warning: 1 } as const
  return (issues ?? [])
    .filter(
      (i) =>
        i.kind === ref.kind &&
        (i.group ?? '') === ref.group &&
        (i.namespace ?? '') === ref.namespace &&
        i.name === ref.name,
    )
    .sort((a, b) => rank[a.severity] - rank[b.severity])
}

export function problemsForObject(issues: CNPGWorkspaceIssue[] | undefined, ref: CNPGObjectRef): CNPGProblem[] {
  return issuesForObject(issues, ref).map((issue) => ({
    id: `${issue.id}:${issue.kind}/${issue.name}`,
    severity: issue.severity,
    category: cnpgIssueCategory(issue),
    title: issue.message || issue.reason,
    detail: issue.cause || undefined,
    subject: { kind: issue.kind, group: issue.group ?? '', namespace: issue.namespace ?? '', name: issue.name },
    source: 'issue',
  }))
}

// ---------------------------------------------------------------------------
// Backups and schedules
// ---------------------------------------------------------------------------

function scheduleOwnerRefs(backup: any): any[] {
  const refs = backup?.metadata?.ownerReferences
  if (!Array.isArray(refs)) return []
  return refs.filter((r: any) => r?.kind === 'ScheduledBackup' && isApiGroup(r?.apiVersion, CNPG_GROUP))
}

/**
 * The name of the ScheduledBackup that created a Backup. The owner reference
 * is only set when the schedule's `backupOwnerReference` is `self`; the
 * operator labels every Backup it creates from a schedule regardless, so the
 * label is the fallback when no such owner reference exists.
 */
export function scheduledBackupOf(backup: any): string | null {
  const owner = scheduleOwnerRefs(backup)[0]
  if (owner?.name) return owner.name
  const label = backup?.metadata?.labels?.[SCHEDULED_BACKUP_LABEL]
  return typeof label === 'string' && label ? label : null
}

/**
 * Whether this schedule created the Backup. An owner reference must match by
 * uid: a schedule deleted and recreated under the same name did not create the
 * old one's Backups. The label, which carries only a name, is read only when
 * no ScheduledBackup owner reference exists.
 */
export function isBackupFromSchedule(backup: any, schedule: any): boolean {
  if (!isCNPGKind(backup, 'Backup') || nsOf(backup) !== nsOf(schedule)) return false
  const owners = scheduleOwnerRefs(backup)
  if (owners.length > 0) {
    const uid = schedule?.metadata?.uid
    return owners.some((r: any) => r?.name === nameOf(schedule) && !!uid && r?.uid === uid)
  }
  return backup?.metadata?.labels?.[SCHEDULED_BACKUP_LABEL] === nameOf(schedule)
}

/** Backups a ScheduledBackup created, newest first. */
export function backupsForScheduledBackup(schedule: any, backups: any[]): any[] {
  return backups.filter((b) => isBackupFromSchedule(b, schedule)).sort((a, b) => backupTime(b) - backupTime(a))
}

export function backupTime(backup: any): number {
  return parseTime(backup?.status?.startedAt) || parseTime(backup?.metadata?.creationTimestamp)
}

/**
 * The ObjectStore a barman-cloud plugin Backup wrote to. The Backup's own
 * plugin parameters are a record of that run; the target Cluster's plugin
 * configuration is only what it is configured with now, so a store taken from
 * there is marked inferred.
 */
export function objectStoreForBackup(backup: any, clusters: any[]): { name: string; inferred: boolean } | null {
  const method = backup?.status?.method || backup?.spec?.method
  if (method !== 'plugin') return null
  const cfg = backup?.spec?.pluginConfiguration
  if (cfg?.name !== CNPG_BARMAN_PLUGIN_NAME) return null
  const own = cfg?.parameters?.barmanObjectName
  if (typeof own === 'string' && own) return { name: own, inferred: false }
  const cluster = targetCluster(backup, clusters)
  const current = cluster ? getCNPGClusterBarmanPlugin(cluster)?.barmanObjectName : undefined
  return current ? { name: current, inferred: true } : null
}

export type CNPGBackupDestination =
  | { type: 'objectStore'; name: string; inferred: boolean }
  | { type: 'path'; path: string }
  | { type: 'volumeSnapshot' }
  | { type: 'unknown' }

export function backupDestination(backup: any, clusters: any[]): CNPGBackupDestination {
  const store = objectStoreForBackup(backup, clusters)
  if (store) return { type: 'objectStore', ...store }
  const method = backup?.status?.method || backup?.spec?.method
  if (method === 'volumeSnapshot') return { type: 'volumeSnapshot' }
  const path = backup?.status?.destinationPath
  if (typeof path === 'string' && path) return { type: 'path', path }
  return { type: 'unknown' }
}

// ---------------------------------------------------------------------------
// ObjectStore
// ---------------------------------------------------------------------------

export interface CNPGObjectStoreUser {
  cluster: any
  /** Key of this cluster's archive inside the store's recovery windows. */
  serverName: string
}

/** Clusters in the store's namespace whose barman-cloud plugin archives to it. */
export function usersOfObjectStore(store: any, clusters: any[]): CNPGObjectStoreUser[] {
  const ns = nsOf(store)
  const name = nameOf(store)
  const out: CNPGObjectStoreUser[] = []
  for (const c of clusters) {
    if (!isCNPGKind(c, 'Cluster') || nsOf(c) !== ns) continue
    const plugin = getCNPGClusterBarmanPlugin(c)
    if (plugin?.barmanObjectName !== name) continue
    out.push({ cluster: c, serverName: plugin.serverName || nameOf(c) })
  }
  return out.sort((a, b) => nameOf(a.cluster).localeCompare(nameOf(b.cluster)))
}

export interface CNPGObjectStoreEvidence {
  cluster: CNPGObjectRef
  serverName: string
  archiving: CNPGFact
  window: CNPGObjectStoreRecoveryWindow | null
}

export interface CNPGObjectStoreHealth {
  summary: CNPGFact
  evidence: CNPGObjectStoreEvidence[]
}

function archivingFact(cluster: any): CNPGFact {
  const conds = cluster?.status?.conditions
  const c = Array.isArray(conds) ? conds.find((x: any) => x?.type === 'ContinuousArchiving') : null
  if (!c) return { text: 'WAL archiving not reported', tone: 'unknown' }
  if (c.status === 'True') return { text: 'WAL archiving', tone: 'healthy', at: c.lastTransitionTime }
  if (c.status === 'False') {
    return { text: c.message ? `WAL archiving failing · ${c.message}` : 'WAL archiving failing', tone: 'unhealthy', at: c.lastTransitionTime }
  }
  return { text: 'WAL archiving unknown', tone: 'unknown' }
}

/**
 * Upload health for an ObjectStore, inferred from the clusters that use it.
 * ObjectStore publishes no health of its own, so the only evidence is each
 * user cluster's ContinuousArchiving condition and whether the store's
 * recovery window for that cluster records a failure newer than its last
 * success.
 */
export function inferredObjectStoreHealth(store: any, users: CNPGObjectStoreUser[]): CNPGObjectStoreHealth {
  const windows = getCNPGObjectStoreRecoveryWindows(store)
  const evidence: CNPGObjectStoreEvidence[] = users.map((u) => ({
    cluster: refOf(u.cluster, 'Cluster'),
    serverName: u.serverName,
    archiving: archivingFact(u.cluster),
    window: windows.find((w) => w.server === u.serverName) ?? null,
  }))
  if (evidence.length === 0) {
    return { summary: { text: 'No visible cluster uses this store', tone: 'unknown' }, evidence }
  }
  const failing = evidence.filter((e) => e.archiving.tone === 'unhealthy' || e.window?.failingSinceLastSuccess)
  if (failing.length > 0) {
    const latestFailure = failing
      .map((e) => e.window?.lastFailedBackupTime ?? (e.archiving.tone === 'unhealthy' ? e.archiving.at : undefined))
      .filter((t): t is string => !!t)
      .sort((a, b) => parseTime(b) - parseTime(a))[0]
    return {
      summary: {
        text: failing.length === evidence.length ? 'Uploads failing' : `Uploads failing for ${failing.length} of ${evidence.length} clusters`,
        tone: 'unhealthy',
        at: latestFailure,
      },
      evidence,
    }
  }
  if (evidence.every((e) => e.archiving.tone === 'healthy')) {
    return { summary: { text: 'Uploads succeeding', tone: 'healthy' }, evidence }
  }
  return { summary: { text: 'No failures reported', tone: 'unknown' }, evidence }
}

// ---------------------------------------------------------------------------
// Declarative objects
// ---------------------------------------------------------------------------

export function appliedFact(obj: any): CNPGFact {
  const applied = obj?.status?.applied
  if (applied === true) return { text: 'Applied', tone: 'healthy' }
  if (applied === false) return { text: 'Not applied', tone: 'unhealthy' }
  return { text: 'Pending · the operator has not reported a result yet', tone: 'unknown' }
}

export function observedGenerationFact(obj: any): CNPGFact {
  const observed = obj?.status?.observedGeneration
  const generation = obj?.metadata?.generation
  if (typeof observed !== 'number') return { text: 'Not reported', tone: 'unknown' }
  if (typeof generation !== 'number') return { text: `Generation ${observed}`, tone: 'neutral' }
  if (observed >= generation) return { text: `Current · generation ${generation}`, tone: 'neutral' }
  return { text: `Behind · observed ${observed}, declared ${generation}`, tone: 'degraded' }
}

/**
 * A role the operator says is missing, when that role is also absent from the
 * target Cluster's managed roles. Only an adjacent fact: roles may be created
 * outside `spec.managed.roles`.
 */
export function missingManagedRole(obj: any, cluster: any | null): string | null {
  if (!cluster) return null
  const msg = obj?.status?.message
  if (typeof msg !== 'string') return null
  const m = msg.match(/role "([^"]+)" does not exist/)
  if (!m) return null
  const roles = cluster?.spec?.managed?.roles
  const names = Array.isArray(roles) ? roles.map((r: any) => r?.name) : []
  return names.includes(m[1]) ? null : m[1]
}

export { cnpgGitOpsSource as gitopsSourceOf } from './workspace'

/** Publications and Subscriptions on the same Cluster and PostgreSQL database. */
export function replicationForDatabase(
  database: any,
  publications: any[],
  subscriptions: any[],
): { publications: any[]; subscriptions: any[] } {
  const ns = nsOf(database)
  const cluster = specCluster(database)
  const dbname = database?.spec?.name
  const match = (o: any, kind: string) =>
    isCNPGKind(o, kind) && !!cluster && nsOf(o) === ns && specCluster(o) === cluster && !!dbname && o?.spec?.dbname === dbname
  return {
    publications: publications.filter((p) => match(p, 'Publication')),
    subscriptions: subscriptions.filter((s) => match(s, 'Subscription')),
  }
}

/** The Database object declaring the PostgreSQL database a Publication/Subscription runs in. */
export function databaseForDeclaration(obj: any, databases: any[]): any | null {
  const cluster = specCluster(obj)
  const dbname = obj?.spec?.dbname
  if (!cluster || !dbname) return null
  return (
    databases.find(
      (d) => isCNPGKind(d, 'Database') && nsOf(d) === nsOf(obj) && specCluster(d) === cluster && d?.spec?.name === dbname,
    ) ?? null
  )
}

// ---------------------------------------------------------------------------
// Image catalogs
// ---------------------------------------------------------------------------

export interface CNPGImageCatalogUser {
  cluster: any
  major: number | null
}

/**
 * Clusters pinned to a catalog through `spec.imageCatalogRef`. An ImageCatalog
 * is namespace-local; a ClusterImageCatalog is referenceable from any
 * namespace. A ref without `kind` means ImageCatalog.
 */
export function clustersUsingCatalog(catalog: any, clusters: any[]): CNPGImageCatalogUser[] {
  const kind = catalog?.kind
  if (kind !== 'ImageCatalog' && kind !== 'ClusterImageCatalog') return []
  if (!isApiGroup(catalog?.apiVersion, CNPG_GROUP)) return []
  const name = nameOf(catalog)
  return clusters
    .filter((c) => {
      if (!isCNPGKind(c, 'Cluster')) return false
      const ref = c?.spec?.imageCatalogRef
      if (!ref?.name || ref.name !== name) return false
      if ((ref.kind || 'ImageCatalog') !== kind) return false
      return kind === 'ClusterImageCatalog' || nsOf(c) === nsOf(catalog)
    })
    .map((c) => {
      const major = c?.spec?.imageCatalogRef?.major
      return { cluster: c, major: typeof major === 'number' ? major : null }
    })
    .sort((a, b) => nsOf(a.cluster).localeCompare(nsOf(b.cluster)) || nameOf(a.cluster).localeCompare(nameOf(b.cluster)))
}
