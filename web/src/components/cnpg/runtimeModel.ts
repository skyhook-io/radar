import { cnpgLagTone, worseTone, type HealthLevel } from '@skyhook-io/k8s-ui'
import type { CNPGRuntimeInstance, CNPGRuntimeReplication } from '../../api/cnpg'
import type { CNPGSessionsResponse } from '../../api/cnpg-sessions'
import { cnpgConnectionFigure } from './blocking'

type Checkpoints = NonNullable<CNPGRuntimeInstance['metrics']['checkpoints']>

/**
 * How to read one instance's checkpoint counters. Restartpoints exist as
 * their own counters only in pg_stat_checkpointer (PostgreSQL 17+), and matter
 * on a standby or where any were done. Pressure needs enough checkpoints for
 * the requested share to mean something.
 */
const CNPG_CHECKPOINT_SHARE_MIN = 10

export function cnpgCheckpointView(c: Checkpoints, role: CNPGRuntimeInstance['role']) {
  const total = c.timed !== undefined && c.requested !== undefined ? c.timed + c.requested : undefined
  const requestedShare = total ? (c.requested as number) / total : undefined
  const few = total !== undefined && total < CNPG_CHECKPOINT_SHARE_MIN
  return {
    total,
    requestedShare,
    // Too few checkpoints for a share to mean anything; the counts beside it say it all.
    share: requestedShare === undefined || few ? undefined : `${(requestedShare * 100).toFixed(0)} %`,
    pressure: requestedShare !== undefined && !few && requestedShare > 0.5,
    showRestartpoints: c.source === 'pg_stat_checkpointer' && (role !== 'primary' || !!c.restartpointsDone),
  }
}

export interface CNPGDatabaseHealthRow {
  database: string
  /** rollbacks / (commits + rollbacks); undefined when either is unreported or there were none. */
  rollbackRatio?: number
  /** Commits and rollbacks were both reported and both zero, so there is no ratio to show. */
  noTransactions?: boolean
  tempFiles?: number
  tempBytes?: number
  xidAge?: number
  mxidAge?: number
}

/** One row per database any family names; a value a family did not report stays undefined. */
export function cnpgDatabaseHealthRows(m: CNPGRuntimeInstance['metrics']): CNPGDatabaseHealthRow[] {
  const rows = new Map<string, CNPGDatabaseHealthRow>()
  const row = (database: string) => {
    let r = rows.get(database)
    if (!r) rows.set(database, (r = { database }))
    return r
  }
  for (const d of m.databases ?? []) {
    const r = row(d.database)
    if (d.xactCommit !== undefined && d.xactRollback !== undefined) {
      if (d.xactCommit + d.xactRollback > 0) r.rollbackRatio = d.xactRollback / (d.xactCommit + d.xactRollback)
      else r.noTransactions = true
    }
    r.tempFiles = d.tempFiles
    r.tempBytes = d.tempBytes
  }
  for (const x of m.xidAge ?? []) row(x.database).xidAge = x.age
  for (const x of m.mxidAge ?? []) row(x.database).mxidAge = x.age
  return [...rows.values()].sort((a, b) => a.database.localeCompare(b.database))
}

// 16 MiB is one WAL segment: a standby a segment or more behind is visibly
// catching up, not just between acknowledgements.
export const CNPG_BACKLOG_DEGRADED = 16 * 1024 * 1024
const CNPG_BACKLOG_UNHEALTHY = 1024 * 1024 * 1024

/** A standby's catch-up tone: the worse of its byte backlog and replay delay, the delay on the same scale as the cluster's Replication fact. */
export function cnpgStandbyBacklogTone(bytes: number | undefined, replayLag: number | undefined): HealthLevel {
  if (bytes === undefined) return 'unknown'
  const byBytes: HealthLevel = bytes >= CNPG_BACKLOG_UNHEALTHY ? 'unhealthy' : bytes >= CNPG_BACKLOG_DEGRADED ? 'degraded' : 'healthy'
  return replayLag === undefined ? byBytes : worseTone(byBytes, cnpgLagTone(replayLag))
}

export interface CNPGStandbyHeadline {
  text: string
  tone: HealthLevel
  /** The streaming state when the headline says something more pressing. */
  secondary?: string
}

/**
 * What a standby card leads with. A paused replay leads: the standby keeps
 * streaming and receiving WAL, so "streaming" alone reads as healthy while
 * nothing is being applied.
 */
export function cnpgStandbyHeadline(
  inst: CNPGRuntimeInstance,
  rep: CNPGRuntimeReplication | undefined,
  backlogTone: HealthLevel,
  ctx: { fenced: boolean; primaryRead: boolean },
): CNPGStandbyHeadline {
  const streaming = rep ? [rep.state, rep.syncState].filter(Boolean).join(' · ') : undefined
  if (inst.status.roleDetail === 'replayPaused' || inst.status.replayPaused) {
    const secondary = streaming ?? (ctx.primaryRead && !ctx.fenced ? 'not connected to the primary' : undefined)
    return { text: 'replay paused', tone: worseTone('degraded', backlogTone), secondary }
  }
  if (streaming !== undefined) return { text: streaming, tone: backlogTone }
  // The fence asks the operator to stop PostgreSQL; only the instance manager
  // reporting it down confirms the shutdown.
  if (ctx.fenced) {
    if (inst.status.state === 'ok') return { text: 'fenced · PostgreSQL still answering', tone: 'unknown' }
    if (/PostgreSQL is not running/.test(inst.status.error ?? '')) return { text: 'fenced · PostgreSQL not running', tone: 'unknown' }
    return { text: 'fenced · shutdown unverified', tone: 'unknown' }
  }
  if (inst.role === 'unknown') return { text: 'role unknown', tone: 'unknown' }
  // A standby the primary reports no row for receives nothing: a concern, not an unknown.
  return ctx.primaryRead ? { text: 'not connected to the primary', tone: 'degraded' } : { text: 'unknown', tone: 'unknown' }
}

type PromLatest = { value: number; at: number; stale: boolean } | undefined

export interface CNPGTransactionRates {
  /** prometheus: cluster-wide, from history; sampled: the primary's, sampled by this page; none: neither is current. */
  source: 'prometheus' | 'sampled' | 'none'
  commits: string
  rollbacks: string
  /** Unix seconds of the Prometheus point shown, or of the last one when it is no longer recent. */
  at?: number
}

/**
 * Commit and rollback rates for the Transactions card. A current Prometheus
 * rate wins; otherwise the page's own sampling when it can sample
 * (`sampled` is null when it cannot: not the primary, or no exporter access);
 * a Prometheus series that stopped is reported as such, never as current.
 */
export function cnpgTransactionRates(
  prom: { commits: PromLatest; rollbacks: PromLatest },
  sampled: { commits?: number; rollbacks?: number } | null,
  cannotSample: string,
): CNPGTransactionRates {
  const fresh = [prom.commits, prom.rollbacks].filter((p) => p && !p.stale) as NonNullable<PromLatest>[]
  if (fresh.length) {
    const text = (p: PromLatest) => (p && !p.stale ? p.value.toFixed(1) : '—')
    return { source: 'prometheus', commits: text(prom.commits), rollbacks: text(prom.rollbacks), at: Math.min(...fresh.map((p) => p.at)) }
  }
  if (sampled) {
    const text = (v?: number) => (v !== undefined ? v.toFixed(1) : 'collecting…')
    return { source: 'sampled', commits: text(sampled.commits), rollbacks: text(sampled.rollbacks) }
  }
  const stale = [prom.commits, prom.rollbacks].filter(Boolean) as NonNullable<PromLatest>[]
  if (stale.length) return { source: 'none', commits: 'no recent sample', rollbacks: 'no recent sample', at: Math.max(...stale.map((p) => p.at)) }
  return { source: 'none', commits: cannotSample, rollbacks: cannotSample }
}

/** The instance Sessions and Transactions read: the one in the URL, else the primary, else the first, so standbys stay reachable without a primary. */
export function cnpgPickedInstance(instances: CNPGRuntimeInstance[], param: string | null): CNPGRuntimeInstance | undefined {
  return instances.find((i) => i.pod === param) ?? instances.find((i) => i.role === 'primary') ?? instances[0]
}

/** Why the exporter's session counts are not shown, or undefined when they were measured. */
export function cnpgSessionAggregatesGap(inst: CNPGRuntimeInstance | undefined): string | undefined {
  if (!inst) return 'no instance is reported'
  const m = inst.metrics
  if (m.state !== 'ok') return `the metrics exporter on ${inst.pod} did not answer${m.error ? ` (${m.error})` : ''}`
  if (m.sessionsTotal === undefined) return `the metrics exporter on ${inst.pod} reported no session counts`
  return undefined
}

/** Whether the Sessions card shows the connections figure, so the Blocking panel does not repeat it. */
export function cnpgSessionsCardShowsConnections(inst: CNPGRuntimeInstance | undefined, exec: CNPGSessionsResponse | undefined): boolean {
  return inst?.metrics.state === 'ok' && cnpgConnectionFigure(exec, inst.metrics) !== undefined
}

/** A transaction or multixact ID age as a readable count: 812, 1.2k, 45k, 3.4 M, 1.1 B. A non-zero age never reads as zero. */
export function cnpgIdAge(v: number): string {
  const unit = (n: number, d: number, s: string) => {
    const x = n / d
    return `${x < 10 ? x.toFixed(1).replace(/\.0$/, '') : Math.round(x)}${s}`
  }
  if (v < 1_000) return String(v)
  if (v < 1_000_000) return unit(v, 1_000, 'k')
  if (v < 1_000_000_000) return unit(v, 1_000_000, ' M')
  return unit(v, 1_000_000_000, ' B')
}

/** Why no standby is listed: a one-instance cluster has none, a larger one is missing them. */
export function cnpgNoStandbyText(desired: unknown): string {
  if (typeof desired !== 'number' || desired <= 1) return 'Single instance: no replica to fail over to.'
  const expected = desired - 1
  return `No standby is running: spec.instances is ${desired}, so ${expected === 1 ? 'one standby is' : `${expected} standbys are`} expected. Nothing to fail over to until one joins.`
}
