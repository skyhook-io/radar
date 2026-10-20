import {
  CNPG_SLOT_RETENTION_WARNING_BYTES,
  cnpgFormatLag,
  cnpgHASlotInstance,
  cnpgReplicationTone,
  cnpgSlotRetentionProblem,
  cnpgStandbyNotReceivingProblem,
  cnpgWithProblems,
  formatGrant,
  type CNPGFleetRow,
  type CNPGProblem,
  type CNPGInstanceLive,
  type CNPGReplicationLive,
  type CNPGSlotRetention,
  type CNPGStandbyGap,
} from '@skyhook-io/k8s-ui'
import type { CNPGRuntimeResponse } from '../../api/cnpg'

/** The instance-manager facts the HA section shows, from a runtime read. */
export function cnpgInstanceLive(rt: CNPGRuntimeResponse | undefined): CNPGInstanceLive[] | undefined {
  if (!rt || rt.permission.proxy === 'denied') return undefined
  return rt.instances.map((i) => ({
    pod: i.pod,
    state: i.status.state,
    pendingRestart: i.status.pendingRestart,
    pendingRestartForDecrease: i.status.pendingRestartForDecrease,
    roleDetail: i.status.roleDetail,
    instanceManagerVersion: i.status.instanceManagerVersion,
    timeline: i.status.timeline,
    incomplete: i.status.incomplete,
    reason: i.status.error ?? i.status.reason,
  }))
}

/** Why the HA section has no instance-manager facts at all; undefined when it has them. */
export function cnpgInstanceLiveUnavailable(rt: CNPGRuntimeResponse | undefined, error: unknown): string | undefined {
  if (rt?.permission.proxy === 'denied') return `needs ${formatGrant(rt.permission.grant) ?? 'get pods/proxy'}`
  if (rt) return undefined
  return error instanceof Error ? `the runtime read failed: ${error.message}` : 'instance managers not read yet'
}

/** Why cnpgReplicationLive has no answer; undefined when it has one. */
export function cnpgReplicationGap(rt: CNPGRuntimeResponse | undefined, error: unknown): string | undefined {
  const unavailable = cnpgInstanceLiveUnavailable(rt, error)
  if (unavailable) return unavailable
  const primary = rt?.instances.find((i) => i.role === 'primary')
  if (!primary) return 'no instance reports being the primary'
  if (primary.status.state !== 'ok' && primary.status.state !== 'partial') {
    return `${primary.pod} did not report (${primary.status.error ?? primary.status.reason ?? primary.status.state})`
  }
  if (!primary.status.replication) return `${primary.pod} did not report its replication rows`
  return undefined
}

/** Streaming standbys and the worst replay lag, from the primary's pg_stat_replication. */
export function cnpgReplicationLive(rt: CNPGRuntimeResponse | undefined): CNPGReplicationLive | undefined {
  const primary = rt?.instances.find((i) => i.role === 'primary')
  if (!primary || (primary.status.state !== 'ok' && primary.status.state !== 'partial')) return undefined
  const reps = primary.status.replication
  if (!reps) return undefined
  const lags = reps.map((r) => r.replayLag).filter((v): v is number => v !== undefined)
  return {
    streaming: reps.filter((r) => r.state === 'streaming').length,
    standbys: rt!.instances.filter((i) => i.role !== 'primary').length,
    maxReplayLagSeconds: lags.length ? Math.max(...lags) : undefined,
  }
}

const LIVE_STANDBY_SOURCE = 'The primary’s pg_stat_replication and each standby’s /pg/status, read through the instance manager'

/** Instances named in the Cluster's cnpg.io/fencedInstances annotation; '*' fences all. */
function fencedInstances(cluster: any): { all: boolean; pods: Set<string> } {
  const raw = cluster?.metadata?.annotations?.['cnpg.io/fencedInstances']
  try {
    const list = raw ? JSON.parse(raw) : []
    if (!Array.isArray(list)) return { all: false, pods: new Set() }
    return { all: list.includes('*'), pods: new Set(list.filter((x: unknown): x is string => typeof x === 'string')) }
  } catch {
    return { all: false, pods: new Set() }
  }
}

/**
 * Standbys the live read shows receiving nothing: no row in the primary's
 * pg_stat_replication although the standby itself answered. A fenced standby
 * is stopped on purpose and one running pg_rewind is rejoining, so neither is
 * a gap.
 */
export function cnpgLiveStandbyGaps(row: CNPGFleetRow, rt: CNPGRuntimeResponse | undefined): CNPGStandbyGap[] {
  const primary = rt?.instances.find((i) => i.role === 'primary')
  const reps = primary?.status.state === 'ok' ? primary.status.replication : undefined
  if (!rt || !primary || !reps || row.hibernated) return []
  const fenced = fencedInstances(row.cluster)
  if (fenced.all) return []
  const connected = new Set(reps.map((r) => r.applicationName))
  const standbys = rt.instances.filter((i) => i.role !== 'primary')
  const gaps = standbys.filter(
    (i) => !connected.has(i.pod) && !fenced.pods.has(i.pod) && (i.status.state === 'ok' || i.status.state === 'partial') && i.status.roleDetail !== 'pgRewind',
  )
  const expected = row.instances.desired !== null ? Math.max(0, row.instances.desired - 1) : standbys.length
  return gaps.map((i) => {
    const evidence = ['it has no row in the primary’s pg_stat_replication']
    if (i.status.isWalReceiverActive === false || i.status.roleDetail === 'fileBased') evidence.push('it reports no active WAL receiver')
    if (i.status.replayPaused || i.status.roleDetail === 'replayPaused') evidence.push('its WAL replay is paused')
    if (i.status.timeline !== undefined && primary.status.timeline !== undefined && i.status.timeline !== primary.status.timeline) {
      evidence.push(`it is on timeline ${i.status.timeline} while the primary is on ${primary.status.timeline}`)
    }
    return {
      pod: i.pod,
      evidence,
      measuredBy: 'the instance manager',
      sourceDetail: LIVE_STANDBY_SOURCE,
      noneReceiving: expected > 0 && reps.length === 0,
    }
  })
}

/** Inactive physical slots on the primary that hold at least the warning amount of WAL. */
export function cnpgLiveSlotRetentions(row: CNPGFleetRow, rt: CNPGRuntimeResponse | undefined): CNPGSlotRetention[] {
  const primary = rt?.instances.find((i) => i.role === 'primary')
  const slots = primary && (primary.status.state === 'ok' || primary.status.state === 'partial') ? primary.status.slots : undefined
  if (!primary || !slots) return []
  const instances = (rt?.instances ?? []).map((i) => i.pod)
  return slots
    .filter((sl): sl is typeof sl & { retainedBytes: number } => sl.active === false && sl.type === 'physical' && sl.retainedBytes !== undefined && sl.retainedBytes >= CNPG_SLOT_RETENTION_WARNING_BYTES)
    .map((sl) => ({
      slot: sl.name,
      pod: primary.pod,
      bytes: sl.retainedBytes,
      standby: cnpgHASlotInstance(row.cluster, sl.name, instances),
      measuredBy: 'the instance manager',
      sourceDetail: 'The primary’s /pg/status replication slots (active) and the exporter’s retained WAL',
    }))
}

/**
 * The fleet row with what the instance managers report: the live replication
 * fact, standbys that receive nothing, and inactive slots. A complete live read
 * replaces the fleet's (Prometheus) answer for what it covers, clearing a
 * problem it disproves — a standby now connected, a slot no longer inactive.
 */
export function withLiveReplication(row: CNPGFleetRow, rt: CNPGRuntimeResponse | undefined): CNPGFleetRow {
  return withLiveSlots(withLiveStandbys(row, rt), rt)
}

// Replaces the Kubernetes-only replication fact with the primary's
// pg_stat_replication when it has been read; otherwise keeps "lag unknown".
// Expected standbys are spec.instances − 1: a standby whose Pod is gone is
// missing, not absent from the count.
function withLiveStandbys(row: CNPGFleetRow, rt: CNPGRuntimeResponse | undefined): CNPGFleetRow {
  const primary = rt?.instances.find((i) => i.role === 'primary')
  if (!primary || primary.status.state !== 'ok' || row.instances.desired === 1 || row.hibernated) return row
  const reps = primary.status.replication
  if (!reps) return row
  const streaming = reps.filter((r) => r.state === 'streaming').length
  const lags = reps.map((r) => r.replayLag).filter((v): v is number => v !== undefined)
  const maxLag = lags.length ? Math.max(...lags) : undefined
  const source = 'From the primary’s pg_stat_replication via the instance manager'
  // A standby that isn't connected has no row, so its delay isn't in the max.
  const lagFor = (allConnected: boolean) =>
    maxLag === undefined ? '' : ` · max replay delay ${cnpgFormatLag(maxLag)}${allConnected ? '' : ' (connected standbys only)'}`
  const gaps = cnpgLiveStandbyGaps(row, rt)
  const connected = new Set(reps.map((r) => r.applicationName))
  // The fleet's standby problem for one the primary now streams to is disproven.
  const disproven = (p: CNPGProblem) => p.reason === 'CNPGStandbyNotReceiving' && connected.has(p.subject.name)
  const raised = gaps.map((g) => cnpgStandbyNotReceivingProblem(row, g))
  if (row.instances.desired === null) {
    const live = { ...row, replication: { text: `${streaming} streaming${lagFor(false)}`, tone: 'unknown' as const, source: `${source}; spec.instances is not reported, so the expected standbys are unknown`, at: primary.status.capturedAt } }
    return cnpgWithProblems(live, raised, disproven)
  }
  const expected = Math.max(0, row.instances.desired - 1)
  const lagText = lagFor(reps.length >= expected)
  const tone = cnpgReplicationTone(streaming, expected, maxLag)
  const text = streaming < expected ? `${streaming} of ${expected} expected standbys streaming` : `${streaming}/${expected} streaming`
  const missing = gaps.length > 0 ? ` · ${gaps.map((g) => g.pod).join(', ')} not connected` : ''
  const live = { ...row, replication: { text: `${text}${missing}${lagText}`, tone, source, at: primary.status.capturedAt } }
  return cnpgWithProblems(live, raised, disproven)
}

// Slots are judged from the primary's own slot list whenever it was read,
// whatever the replication rows say. A fleet warning is cleared only by
// evidence against it: the slot active, measured below the threshold, or
// absent from a complete list. An unmeasured slot or a capped, partial list
// leaves it standing.
function withLiveSlots(row: CNPGFleetRow, rt: CNPGRuntimeResponse | undefined): CNPGFleetRow {
  const primary = rt?.instances.find((i) => i.role === 'primary')
  const slots = primary && (primary.status.state === 'ok' || primary.status.state === 'partial') ? primary.status.slots : undefined
  if (!primary || !slots) return row
  const complete = primary.status.state === 'ok'
  const byName = new Map(slots.map((sl) => [sl.name, sl]))
  const disproven = (p: CNPGProblem) => {
    if (p.reason !== 'CNPGInactiveSlot' || !p.slot) return false
    const sl = byName.get(p.slot)
    if (!sl) return complete
    return sl.active === true || (sl.retainedBytes !== undefined && sl.retainedBytes < CNPG_SLOT_RETENTION_WARNING_BYTES)
  }
  return cnpgWithProblems(row, cnpgLiveSlotRetentions(row, rt).map((r) => cnpgSlotRetentionProblem(row, r)), disproven)
}
