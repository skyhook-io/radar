// High-availability facts for one CloudNativePG Cluster, from
// GET /api/cnpg/clusters/{ns}/{name}/ha, plus pure derivations the Overview and
// the switchover dialog share. Each sub-read carries its own state: a denied or
// unavailable source is "unknown", never none or healthy.

import type { HealthLevel } from '../resources/resource-utils'
import { formatGrant, type Grant } from '../../utils/grant'
import { cnpgFormatLag, cnpgLagTone, cnpgReplicationTone, cnpgSustainedLagProblemId, type CNPGFleetRow } from './workspace'
import type { Fact, FoldSummary } from '../workspace'
import { worseTone } from '../ui/status-tone'

export type CNPGHASourceState = 'ok' | 'denied' | 'notFound' | 'notInstalled' | 'unavailable' | 'error'

export interface CNPGHASource {
  state: CNPGHASourceState
  reason?: string
  grant?: Grant
}

export interface CNPGHAInstance {
  pod: string
  podUID: string
  role: 'primary' | 'replica' | 'unknown'
  ready: boolean
  node?: string
  zone?: string
  qosClass?: string
  image?: string
  imageMatches?: boolean
  podCreatedAt?: string
  postgresStartedAt?: string
  restartCount: number
}

export interface CNPGHAQuorum {
  enabled: boolean
  enabledBy?: 'spec' | 'annotation'
  method?: string
  number?: number
  dataDurability?: string
  object: CNPGHASource
  status?: { method?: string; standbyNames: string[]; standbyNumber: number; primary?: string }
  n?: number
  w?: number
  r?: number
  promotable?: string[]
  holds?: boolean
}

export interface CNPGHAPDB {
  name: string
  role: 'primary' | 'replicas' | 'other'
  minAvailable?: string
  maxUnavailable?: string
  expectedPods: number
  currentHealthy: number
  desiredHealthy: number
  disruptionsAllowed: number
  observed: boolean
}

export interface CNPGHALease extends CNPGHASource {
  namespace?: string
  name?: string
  holder?: string
  renewTime?: string
  durationSeconds?: number
  expired?: boolean
  controlledByCluster?: boolean
}

export interface CNPGHAJob {
  name: string
  role?: string
  instance?: string
  phase: 'running' | 'succeeded' | 'failed' | 'pending'
  reason?: string
  startTime?: string
  completionTime?: string
}

export interface CNPGHACertificate {
  secret: string
  purposes?: string[]
  raw: string
  expiresAt?: string
  renewal: 'operator' | 'user'
  metadata?: CNPGHASource
  certManager?: { certificate: string; issuer?: string; issuerKind?: string }
}

export interface CNPGMaintenanceFacts {
  declared: boolean
  inProgress: boolean
  reusePVC: boolean
}

export interface CNPGClusterHA {
  cluster: { namespace: string; name: string; uid: string }
  sampledAt: string
  desiredImage?: string
  instances: CNPGHAInstance[]
  pods: CNPGHASource
  nodes: CNPGHASource
  quorum: CNPGHAQuorum
  pdbs: CNPGHASource & { enabled: boolean; items: CNPGHAPDB[] }
  primaryLease: CNPGHALease
  operatorLease: CNPGHALease
  jobs: CNPGHASource & { items: CNPGHAJob[] }
  rwEndpoints: CNPGHASource & { service: string; pods: string[] }
  certificates: CNPGHACertificate[]
  maintenance: CNPGMaintenanceFacts
}

/**
 * Per-instance facts read live from each instance manager (/pg/status). Only
 * callers who can read them pass them in: they never come from the cached
 * Cluster object.
 */
export interface CNPGInstanceLive {
  pod: string
  /** ok, or why the instance's own report is missing. */
  state: string
  pendingRestart?: boolean
  pendingRestartForDecrease?: boolean
  /** The report did not finish its reads, so pendingRestart is not established. */
  incomplete?: boolean
  /** Why the report is missing or incomplete, in words. */
  reason?: string
  roleDetail?: 'primary' | 'pgRewind' | 'replayPaused' | 'streaming' | 'fileBased'
  instanceManagerVersion?: string
  timeline?: number
}

export const CNPG_ROLE_DETAIL_TEXT: Record<NonNullable<CNPGInstanceLive['roleDetail']>, string> = {
  primary: 'primary',
  pgRewind: 'pg_rewind running',
  replayPaused: 'replay paused',
  streaming: 'streaming standby',
  fileBased: 'file-based standby (no WAL receiver)',
}

export function cnpgHASourceText(src: CNPGHASource | undefined, what: string): string {
  if (!src) return `${what}: unknown`
  switch (src.state) {
    case 'ok':
      return ''
    case 'denied':
      return `No access to ${what}${src.grant ? ` (needs ${formatGrant(src.grant)})` : ''}`
    case 'notInstalled':
      return src.reason ?? `${what}: not available in this CloudNativePG version`
    case 'notFound':
      return src.reason ?? `No ${what}`
    case 'unavailable':
      return src.reason ?? `${what}: unavailable`
    default:
      return src.reason ? `${what} could not be read: ${src.reason}` : `${what} could not be read`
  }
}

// ---------------------------------------------------------------------------
// Failure domains

export interface CNPGZoneSpread {
  /** false when Nodes (or Pods) could not be read: zones are unknown. */
  known: boolean
  zones: { zone: string; pods: string[] }[]
  /** Instances whose Node carries no zone label. */
  unlabelled: string[]
  primaryZone?: string
  /** true when every labelled instance shares one zone (and there is more than one instance). */
  singleZone: boolean
  nodes: { node: string; pods: string[] }[]
  /** true when two or more instances share one Node. */
  sharedNode: boolean
}

export function cnpgZoneSpread(ha: CNPGClusterHA | undefined): CNPGZoneSpread {
  const empty: CNPGZoneSpread = { known: false, zones: [], unlabelled: [], singleZone: false, nodes: [], sharedNode: false }
  if (!ha || ha.pods.state !== 'ok') return empty
  const byNode = new Map<string, string[]>()
  for (const i of ha.instances) {
    if (!i.node) continue
    byNode.set(i.node, [...(byNode.get(i.node) ?? []), i.pod])
  }
  const nodes = [...byNode.entries()].map(([node, pods]) => ({ node, pods })).sort((a, b) => a.node.localeCompare(b.node))
  const sharedNode = nodes.some((n) => n.pods.length > 1)
  if (ha.nodes.state !== 'ok') return { ...empty, nodes, sharedNode }
  const byZone = new Map<string, string[]>()
  const unlabelled: string[] = []
  let primaryZone: string | undefined
  for (const i of ha.instances) {
    if (!i.zone) {
      unlabelled.push(i.pod)
      continue
    }
    byZone.set(i.zone, [...(byZone.get(i.zone) ?? []), i.pod])
    if (i.role === 'primary') primaryZone = i.zone
  }
  const zones = [...byZone.entries()].map(([zone, pods]) => ({ zone, pods })).sort((a, b) => a.zone.localeCompare(b.zone))
  return {
    known: true,
    zones,
    unlabelled,
    primaryZone,
    singleZone: zones.length === 1 && ha.instances.length > 1 && unlabelled.length === 0,
    nodes,
    sharedNode,
  }
}

// ---------------------------------------------------------------------------
// Quorum, PDB, images, certificates

export function cnpgQuorumFact(q: CNPGHAQuorum | undefined): Fact {
  if (!q) return { text: 'Unknown', tone: 'unknown' }
  if (!q.enabled) {
    if (q.number !== undefined || q.method) {
      return { text: `Synchronous ${q.method ?? ''} ${q.number ?? ''}`.replace(/\s+/g, ' ').trim() + ' · quorum failover off', tone: 'neutral', source: 'Cluster spec.postgresql.synchronous' }
    }
    return { text: 'Off (asynchronous replication)', tone: 'neutral', source: 'Cluster spec' }
  }
  const unread = cnpgHASourceText(q.object, 'FailoverQuorum')
  if (q.object.state !== 'ok') return { text: `Quorum failover on · ${unread}`, tone: 'unknown' }
  if (q.n === undefined || q.w === undefined) {
    return {
      text: 'Quorum failover on · no synchronous configuration recorded: a failover would wait',
      tone: 'degraded',
      source: 'FailoverQuorum status (reset while PostgreSQL configuration changes)',
    }
  }
  const base = `W ${q.w} of N ${q.n} potentially synchronous`
  if (q.r === undefined || q.holds === undefined) {
    return { text: `${base} · promotable replicas unknown`, tone: 'unknown', source: 'FailoverQuorum status; Pods not readable' }
  }
  return {
    text: `${base} · R ${q.r} promotable · R + W ${q.holds ? '>' : '≤'} N`,
    tone: q.holds ? 'healthy' : 'degraded',
    source: 'FailoverQuorum status (the recorded configuration, not the operator’s decision); R from ready standby Pods',
  }
}

export function cnpgPDBFact(pdbs: CNPGClusterHA['pdbs'] | undefined): Fact {
  if (!pdbs) return { text: 'Unknown', tone: 'unknown' }
  if (pdbs.state !== 'ok') return { text: cnpgHASourceText(pdbs, 'PodDisruptionBudgets'), tone: 'unknown' }
  if (pdbs.items.length === 0) {
    return pdbs.enabled
      ? { text: 'None found although spec.enablePDB is on', tone: 'degraded', source: 'PodDisruptionBudgets owned by the Cluster' }
      : { text: 'Disabled (spec.enablePDB: false): node drains are not held back', tone: 'neutral', source: 'Cluster spec' }
  }
  const parts = pdbs.items.map((p) => `${p.role === 'primary' ? 'primary' : p.role === 'replicas' ? 'standbys' : p.name}: ${p.disruptionsAllowed} disruption${p.disruptionsAllowed === 1 ? '' : 's'} allowed (${p.currentHealthy}/${p.expectedPods} healthy)`)
  const stale = pdbs.items.some((p) => !p.observed)
  return {
    text: parts.join(' · '),
    tone: stale ? 'unknown' : 'neutral',
    source: stale ? 'PodDisruptionBudget status (not yet updated for the latest spec)' : 'PodDisruptionBudget status',
  }
}

export function cnpgImageDrift(ha: CNPGClusterHA | undefined): { known: boolean; drifted: CNPGHAInstance[] } {
  if (!ha || ha.pods.state !== 'ok' || !ha.desiredImage) return { known: false, drifted: [] }
  return { known: true, drifted: ha.instances.filter((i) => i.imageMatches === false) }
}

export interface CNPGCertificateView extends CNPGHACertificate {
  /** Whole days until expiry; negative when expired; undefined when the expiry did not parse. */
  daysLeft?: number
  tone: HealthLevel
}

/**
 * The same thresholds as the Issues engine: a certificate its owner renews is
 * flagged from 30 days; the operator renews its own, so those only matter once
 * renewal is overdue.
 */
export function cnpgCertificateViews(certs: CNPGHACertificate[] | undefined, now = Date.now()): CNPGCertificateView[] {
  return (certs ?? []).map((c) => {
    if (!c.expiresAt) return { ...c, tone: 'unknown' as HealthLevel }
    const ms = Date.parse(c.expiresAt) - now
    const daysLeft = Math.floor(ms / 86_400_000)
    let tone: HealthLevel = 'healthy'
    if (ms <= 0) tone = 'unhealthy'
    else if (c.renewal === 'user' && ms < 7 * 86_400_000) tone = 'unhealthy'
    else if (c.renewal === 'user' && ms < 30 * 86_400_000) tone = 'degraded'
    else if (c.renewal === 'operator' && ms < 86_400_000) tone = 'unhealthy'
    return { ...c, daysLeft, tone }
  })
}

/**
 * "HA and instances" in one line: what is wrong when something is, otherwise
 * the readiness and placement facts that are known. Unknown facts never read
 * as fine; they are left out of a calm summary rather than claimed.
 */
export function cnpgHASummary(
  ha: CNPGClusterHA | undefined,
  live: CNPGInstanceLive[] | undefined,
  primaryConflict?: { status: string; labelled: string },
): FoldSummary {
  if (!ha) return { text: 'Not read', attention: false }
  const issues: string[] = []
  const calm: string[] = []
  if (ha.pods.state === 'ok') {
    const ready = ha.instances.filter((i) => i.ready).length
    if (ha.instances.length === 0) issues.push('no instance Pods')
    else if (ready < ha.instances.length) issues.push(`${ha.instances.length - ready} of ${ha.instances.length} instances not ready`)
    else calm.push(`${ready}/${ha.instances.length} instances ready`)
  }
  if (primaryConflict) issues.push('primary labels disagree')
  const spread = cnpgZoneSpread(ha)
  if (spread.singleZone) issues.push('every instance in one zone')
  if (spread.sharedNode) issues.push('instances share a Node')
  if (spread.known && !spread.singleZone && spread.zones.length > 1) calm.push(`${spread.zones.length} zones`)
  const pending = cnpgPendingRestart(live)
  if (pending.pods.length > 0) issues.push(`restart pending on ${pending.pods.join(', ')}`)
  const drift = cnpgImageDrift(ha)
  if (drift.drifted.length > 0) issues.push(`${drift.drifted.length === 1 ? 'an instance runs' : `${drift.drifted.length} instances run`} a different image`)
  else if (drift.known && ha.instances.length > 0 && ha.instances.every((i) => i.imageMatches === true)) calm.push('images match')
  const quorum = cnpgQuorumFact(ha.quorum)
  if (quorum.tone === 'degraded' || quorum.tone === 'unhealthy') issues.push('failover quorum does not hold')
  const pdb = cnpgPDBFact(ha.pdbs)
  if (pdb.tone === 'degraded' || pdb.tone === 'unhealthy') issues.push('disruption budgets missing')
  for (const [lease, what] of [[ha.primaryLease, 'primary lease'], [ha.operatorLease, 'operator lease']] as const) {
    if (lease.state === 'ok' && lease.expired) issues.push(`${what} expired`)
  }
  if (ha.primaryLease.state === 'ok' && ha.primaryLease.controlledByCluster === false) issues.push('primary lease not owned by this Cluster')
  const failedJobs = ha.jobs.state === 'ok' ? ha.jobs.items.filter((j) => j.phase === 'failed').length : 0
  if (failedJobs > 0) issues.push(`${failedJobs} failed ${failedJobs === 1 ? 'Job' : 'Jobs'}`)
  // A fact that could not be read is named, never folded into a calm line.
  const unread: string[] = []
  const check = (src: CNPGHASource | undefined, label: string) => {
    if (src && (src.state === 'denied' || src.state === 'unavailable' || src.state === 'error')) unread.push(label)
  }
  check(ha.pods, 'Pods')
  check(ha.nodes, 'zones')
  if (ha.quorum.enabled) check(ha.quorum.object, 'quorum')
  check(ha.pdbs, 'disruption budgets')
  check(ha.primaryLease, 'primary lease')
  check(ha.operatorLease, 'operator lease')
  check(ha.jobs, 'Jobs')
  if (!pending.known && !(ha.pods.state === 'ok' && ha.instances.length === 0)) unread.push('pending restarts')
  const notRead = unread.length > 0 ? `not read: ${unread.join(', ')}` : ''
  if (issues.length > 0) return { text: [...issues, notRead].filter(Boolean).join(' · '), attention: true }
  if (calm.length === 0) return { text: notRead ? notRead[0].toUpperCase() + notRead.slice(1) : 'Nothing reported out of line', attention: false }
  return { text: [...calm, notRead].filter(Boolean).join(' · '), attention: false }
}

/** Certificates in one line: the nearest expiry and who renews them. */
export function cnpgCertificatesSummary(certs: CNPGHACertificate[] | undefined, now = Date.now()): FoldSummary {
  const views = cnpgCertificateViews(certs, now)
  if (views.length === 0) return { text: 'No expiry reported by the operator', attention: false }
  const dated = views.filter((c) => Number.isFinite(c.daysLeft)).sort((a, b) => (a.daysLeft ?? 0) - (b.daysLeft ?? 0))
  const unreadable = views.length - dated.length
  // An unreadable expiry could be past already, so it opens the section too.
  const attention = unreadable > 0 || views.some((c) => c.tone === 'degraded' || c.tone === 'unhealthy')
  const nearest = dated[0]
  const when = [
    !nearest ? '' : nearest.daysLeft! < 0 ? `${nearest.secret} expired` : `nearest reported expiry in ${nearest.daysLeft} d (${nearest.secret})`,
    unreadable > 0 ? `${unreadable} expiry unreadable` : '',
  ]
    .filter(Boolean)
    .join(' · ')
  const renewers = new Set(views.map((c) => c.renewal))
  const who = renewers.size > 1 ? 'some renewed by you' : renewers.has('user') ? 'you renew them' : 'CloudNativePG renews them'
  return { text: `${views.length} ${views.length === 1 ? 'certificate' : 'certificates'} · ${when} · ${who}`, attention }
}

function cnpgLiveRead(l: CNPGInstanceLive): boolean {
  return (l.state === 'ok' || l.state === 'partial') && !l.incomplete
}

export function cnpgPendingRestart(live: CNPGInstanceLive[] | undefined): { known: boolean; pods: string[]; forDecrease: boolean } {
  const read = (live ?? []).filter(cnpgLiveRead)
  if (read.length === 0) return { known: false, pods: [], forDecrease: false }
  const pending = read.filter((l) => l.pendingRestart)
  return { known: read.length === (live ?? []).length, pods: pending.map((l) => l.pod), forDecrease: pending.some((l) => l.pendingRestartForDecrease) }
}

/**
 * Why instance-manager facts are not established: the host's reason when
 * there is no runtime read at all (`unavailable`, e.g. the missing grant),
 * otherwise each instance that did not report and why.
 */
export function cnpgLiveGap(live: CNPGInstanceLive[] | undefined, unavailable?: string): string {
  if (!live) return unavailable ?? 'needs each instance manager’s status (get pods/proxy)'
  if (live.length === 0) return 'no instance was read'
  const unread = live.filter((l) => !cnpgLiveRead(l))
  if (unread.length === 0) return ''
  return unread
    .map((l) => {
      const why = l.reason ?? (l.state === 'denied' ? 'no access' : l.state)
      return l.incomplete ? `${l.pod} reported incompletely (${why})` : `${l.pod} did not report (${why})`
    })
    .join('; ')
}

// ---------------------------------------------------------------------------
// Header dimensions

export type CNPGDimensionId = 'serving' | 'replication' | 'protection' | 'storage'

export interface CNPGDimension {
  id: CNPGDimensionId
  label: string
  /** unknown = unassessed: its source is not available. */
  tone: HealthLevel
  text: string
  source: string
}

export interface CNPGReplicationLive {
  /** Standbys the primary reports as streaming. */
  streaming: number
  /** Standby Pods the runtime read saw; the verdict compares against spec.instances − 1, not this. */
  standbys: number
  maxReplayLagSeconds?: number
}

export function cnpgDimensions({
  row,
  ha,
  replication,
  replicationGap,
  storage,
}: {
  row: CNPGFleetRow
  ha?: CNPGClusterHA
  /** From the primary's pg_stat_replication; absent when runtime data is not readable. */
  replication?: CNPGReplicationLive
  /** Why `replication` is absent (e.g. "needs get pods/proxy in db"). */
  replicationGap?: string
  /** Supplied by the host once storage is assessed; unassessed otherwise. */
  storage?: CNPGDimension
}): CNPGDimension[] {
  return [
    servingDimension(row, ha),
    replicationDimension(row, replication, replicationGap),
    protectionDimension(row),
    storage ?? storageDimension(row),
  ]
}

function storageDimension(row: CNPGFleetRow): CNPGDimension {
  const base = { id: 'storage' as const, label: 'Storage' }
  const disk = row.disk
  if (!disk) return { ...base, tone: 'unknown', text: 'unassessed', source: 'Volume usage is not assessed here' }
  if (disk.tone === 'unknown') return { ...base, tone: 'unknown', text: 'unassessed', source: [disk.text, disk.source].filter(Boolean).join(' · ') }
  return { ...base, tone: disk.tone, text: disk.text, source: disk.source ?? 'Fullest volume' }
}

function servingDimension(row: CNPGFleetRow, ha?: CNPGClusterHA): CNPGDimension {
  const base = { id: 'serving' as const, label: 'Serving' }
  if (row.hibernated) return { ...base, tone: 'neutral', text: 'hibernated', source: 'cnpg.io/hibernation annotation' }
  const primaryName = row.cluster?.status?.currentPrimary as string | undefined
  const primary = row.pods.find((p) => p.name === primaryName)
  if (!primaryName) return { ...base, tone: 'unknown', text: 'unassessed', source: 'No current primary reported' }
  if (!primary || primary.ready === null) return { ...base, tone: 'unknown', text: 'unassessed', source: 'Instance Pods are not readable' }
  if (!primary.ready) return { ...base, tone: 'unhealthy', text: 'primary not ready', source: `Pod ${primaryName} readiness` }
  if (ha?.rwEndpoints.state === 'ok') {
    if (!ha.rwEndpoints.pods.includes(primaryName)) {
      return {
        ...base,
        tone: 'unhealthy',
        text: ha.rwEndpoints.pods.length === 0 ? 'no read-write endpoint' : 'read-write endpoint not on the primary',
        source: `EndpointSlices of Service ${ha.rwEndpoints.service}`,
      }
    }
    return { ...base, tone: 'healthy', text: 'primary ready', source: `Pod ${primaryName} ready and behind Service ${ha.rwEndpoints.service}` }
  }
  return { ...base, tone: 'healthy', text: 'primary ready', source: `Pod ${primaryName} readiness (Service endpoints not readable)` }
}

// A standby measured far behind for the whole window outranks what the live
// read shows: the chip must not read calmer than the finding below it.
function replicationDimension(row: CNPGFleetRow, live?: CNPGReplicationLive, gap?: string): CNPGDimension {
  const dim = liveReplicationDimension(row, live, gap)
  const sustained = row.problems.find((p) => p.id === cnpgSustainedLagProblemId(row.key))
  if (!sustained) return dim
  const tone: HealthLevel = sustained.severity === 'critical' ? 'unhealthy' : 'degraded'
  return {
    ...dim,
    tone: worseTone(dim.tone, tone),
    text: dim.tone === 'unknown' ? 'sustained lag' : `${dim.text} · sustained lag`,
    source: sustained.title,
  }
}

function liveReplicationDimension(row: CNPGFleetRow, live?: CNPGReplicationLive, gap?: string): CNPGDimension {
  const base = { id: 'replication' as const, label: 'Replication' }
  if (row.hibernated) return { ...base, tone: 'neutral', text: 'hibernated', source: 'cnpg.io/hibernation annotation' }
  if (row.instances.desired === 1) return { ...base, tone: 'degraded', text: 'no standby', source: 'spec.instances is 1: there is no failover target' }
  if (!live) return { ...base, tone: 'unknown', text: 'unassessed', source: `Needs the primary’s pg_stat_replication: ${gap ?? 'read through get pods/proxy'}` }
  // Standbys whose Pods are gone are missing from the runtime read too, so
  // the denominator is what the Cluster asks for, never what is running.
  const desired = row.instances.desired
  if (desired === null) {
    return { ...base, tone: 'unknown', text: `${live.streaming} streaming`, source: 'spec.instances is not reported, so the expected standbys are unknown' }
  }
  const expected = Math.max(0, desired - 1)
  const source = `Primary’s pg_stat_replication against spec.instances ${desired}`
  const lag = live.maxReplayLagSeconds
  const tone = cnpgReplicationTone(live.streaming, expected, lag)
  // pg_stat_replication's replay_lag is the recent replay delay, not WAL still
  // to replay; it can stay high after a standby caught up, so it is not "behind".
  const lagText = lag !== undefined && cnpgLagTone(lag) !== 'healthy' ? ` · replay delay ${cnpgFormatLag(lag)}` : ''
  if (live.streaming < expected) {
    return { ...base, tone, text: `${live.streaming} of ${expected} expected standbys streaming${lagText}`, source }
  }
  if (lagText) return { ...base, tone, text: `${live.streaming} of ${expected} streaming${lagText}`, source }
  return { ...base, tone: 'healthy', text: `${live.streaming} of ${expected} standbys streaming`, source }
}

function protectionDimension(row: CNPGFleetRow): CNPGDimension {
  const base = { id: 'protection' as const, label: 'Protection' }
  const p = row.protection
  if (p.walArchiving.tone === 'unhealthy') return { ...base, tone: 'unhealthy', text: 'WAL archiving failing', source: 'ContinuousArchiving condition' }
  if (p.destination.method === 'none') return { ...base, tone: 'degraded', text: 'no backup destination', source: 'Cluster spec' }
  if (p.lastSuccessfulBackup.tone === 'unhealthy' || p.lastSuccessfulBackup.tone === 'degraded') {
    return { ...base, tone: p.lastSuccessfulBackup.tone, text: p.lastSuccessfulBackup.text, source: p.lastSuccessfulBackup.source ?? 'Backups' }
  }
  if (p.walArchiving.tone === 'unknown') return { ...base, tone: 'unknown', text: 'unassessed', source: 'WAL archiving not reported' }
  return { ...base, tone: 'healthy', text: 'archiving', source: 'ContinuousArchiving condition' }
}

/**
 * The Pod a Lease holder names. controller-runtime's leader election records
 * "<pod>_<uuid>"; a CNPG primary Lease records the Pod name alone.
 */
export function cnpgLeaseHolderPod(holder: string): string {
  const i = holder.indexOf('_')
  return i > 0 ? holder.slice(0, i) : holder
}
