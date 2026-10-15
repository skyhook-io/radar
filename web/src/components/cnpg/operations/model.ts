import type { CNPGClusterHA } from '@skyhook-io/k8s-ui'
import type { CNPGClusterFacts, CNPGRuntimeResponse } from '../../../api/cnpg'
import { formatGrant } from '@skyhook-io/k8s-ui'

// Follow-through for CloudNativePG actions. A successful POST only means the
// write was accepted; the operator acts on it later. A tracked operation is
// bound to the context, Cluster UID and target it was requested for, carries a
// baseline taken before the write, and is judged by a per-kind observer
// against what the cluster reports now. Missing telemetry is "unobservable",
// never "stalled": stalled needs evidence that nothing is moving.

export type CNPGOpState = 'requested' | 'observed' | 'progressing' | 'completed' | 'failed' | 'stalled' | 'superseded' | 'unobservable'

export const CNPG_OP_TERMINAL: ReadonlySet<CNPGOpState> = new Set(['completed', 'failed', 'superseded'])

export interface CNPGOpStep {
  label: string
  /** true done, false not yet, null not observable with the caller's access. */
  done: boolean | null
}

export interface CNPGTrackedOperation {
  id: string
  kind: string
  label: string
  context: string
  namespace: string
  cluster: string
  /** Absent when the requester could not read the Cluster identity. */
  clusterUID?: string
  target?: { name: string; uid?: string }
  startedAt: number
  baseline: Record<string, unknown>
  state: CNPGOpState
  detail?: string
  steps?: CNPGOpStep[]
  /** When the observer last saw its progress key change. */
  lastProgressAt: number
  /** Last assessment in this tab, including an assessment with unavailable evidence. */
  lastCheckedAt?: number
  progressKey?: string
  /** A related object to open, e.g. the Backup. */
  link?: { kind: string; group: string; name: string }
  finishedAt?: number
}

/** Everything an observer may look at. Any source can be missing. */
export interface CNPGObservation {
  now: number
  /** The Cluster's UID now, from whichever source read it. */
  clusterUID?: string
  /** The first live identity check after mounting is still in flight. */
  identityPending?: boolean
  facts?: CNPGClusterFacts
  /** The Cluster object from the workspace (status.conditions, readyInstances). */
  cluster?: any
  ha?: CNPGClusterHA
  runtime?: CNPGRuntimeResponse
  /** Backups of the namespace, or undefined when they are not readable. */
  backups?: any[]
  /**
   * When each source was last fetched successfully and whether its latest
   * fetch failed. A source without a record here, not fetched successfully
   * since the operation started, or whose refresh failed is withheld from
   * its observer. Cluster-scoped sources also carry the UID they read, so
   * facts from a replaced Cluster cannot certify the operation's outcome.
   */
  freshness?: Partial<Record<CNPGObservedSource, { updatedAt: number; failed: boolean; clusterUID?: string }>>
}

export type CNPGObservedSource = 'clusterUID' | 'facts' | 'cluster' | 'ha' | 'runtime' | 'backups'

/** The observation with every stale or failed source removed. */
export function freshObservation(op: CNPGTrackedOperation, obs: CNPGObservation): CNPGObservation {
  const out: CNPGObservation = { ...obs }
  for (const source of CNPG_OBSERVED_SOURCES) {
    const f = obs.freshness?.[source]
    const differentCluster = op.clusterUID && source !== 'clusterUID' && source !== 'backups' && f?.clusterUID !== op.clusterUID
    if (!f || f.failed || f.updatedAt < op.startedAt || differentCluster) out[source] = undefined
  }
  return out
}

const CNPG_OBSERVED_SOURCES: readonly CNPGObservedSource[] = ['clusterUID', 'facts', 'cluster', 'ha', 'runtime', 'backups']

export interface CNPGObserverResult {
  state: Exclude<CNPGOpState, 'superseded' | 'stalled'>
  detail?: string
  steps?: CNPGOpStep[]
  /** Changes whenever the operation moves; drives stall detection. */
  progressKey?: string
  /** Nothing will ever report more: stop following now. */
  final?: boolean
}

export type CNPGOperationObserver = (op: CNPGTrackedOperation, obs: CNPGObservation) => CNPGObserverResult

const observers = new Map<string, CNPGOperationObserver>()

/** Plug in the observer for a new operation kind. */
export function registerCNPGOperationObserver(kind: string, observer: CNPGOperationObserver) {
  observers.set(kind, observer)
}

/** No progress for this long, with telemetry present, reads as stalled. */
export const CNPG_OP_STALL_MS = 10 * 60_000

/** An operation Radar cannot observe is followed this long, then left alone. */
export const CNPG_OP_UNOBSERVABLE_MS = 15 * 60_000

/** Whether the tracker should still poll for this operation. */
export function cnpgOperationFollowed(op: CNPGTrackedOperation): boolean {
  return !CNPG_OP_TERMINAL.has(op.state) && !op.finishedAt
}

/** Advance one operation against an observation. Pure. */
export function advanceCNPGOperation(op: CNPGTrackedOperation, obs: CNPGObservation): CNPGTrackedOperation {
  if (CNPG_OP_TERMINAL.has(op.state)) return op
  op = { ...op, lastCheckedAt: obs.now }
  const current = freshObservation(op, obs)
  if (op.clusterUID && current.clusterUID && current.clusterUID !== op.clusterUID) {
    return { ...op, state: 'superseded', detail: 'The Cluster was deleted and recreated; this operation no longer applies to it', finishedAt: obs.now }
  }
  if (op.clusterUID && !current.clusterUID) {
    return {
      ...op,
      state: 'unobservable',
      detail: 'The Cluster identity has not been verified since this operation was requested',
      finishedAt: !obs.identityPending && obs.now - op.startedAt > CNPG_OP_UNOBSERVABLE_MS ? obs.now : undefined,
    }
  }
  const observer = observers.get(op.kind)
  if (!observer) return { ...op, state: 'unobservable', detail: 'Radar has no way to follow this operation' }
  const res = observer(op, current)
  const moved = res.progressKey !== undefined && res.progressKey !== op.progressKey
  const lastProgressAt = moved ? obs.now : op.lastProgressAt
  let state: CNPGOpState = res.state
  let detail = res.detail
  if ((state === 'progressing' || state === 'observed' || state === 'requested') && res.progressKey !== undefined && obs.now - lastProgressAt > CNPG_OP_STALL_MS) {
    state = 'stalled'
    detail = `No progress for ${Math.round((obs.now - lastProgressAt) / 60_000)} min${res.detail ? ` · ${res.detail}` : ''}`
  }
  const givenUp = state === 'unobservable' && (res.final || obs.now - op.startedAt > CNPG_OP_UNOBSERVABLE_MS)
  return {
    ...op,
    state,
    detail,
    steps: res.steps ?? op.steps,
    progressKey: res.progressKey ?? op.progressKey,
    lastProgressAt,
    finishedAt: CNPG_OP_TERMINAL.has(state) || givenUp ? obs.now : undefined,
  }
}

/**
 * A newer operation of a kind that changes the same thing makes an older,
 * unfinished one moot: its expectation will never be met.
 */
const CONFLICTS: Record<string, string[]> = {
  switchover: ['switchover', 'hibernate', 'rehydrate'],
  restart: ['restart', 'hibernate'],
  restartInstance: ['hibernate', 'restart'],
  fence: ['fence', 'unfence', 'hibernate'],
  unfence: ['fence', 'unfence', 'hibernate'],
  hibernate: ['hibernate', 'rehydrate'],
  rehydrate: ['hibernate', 'rehydrate'],
  setMaintenance: ['setMaintenance', 'unsetMaintenance'],
  unsetMaintenance: ['setMaintenance', 'unsetMaintenance'],
}

export function supersedeFor(existing: CNPGTrackedOperation[], next: CNPGTrackedOperation): CNPGTrackedOperation[] {
  return existing.map((op) => {
    if (CNPG_OP_TERMINAL.has(op.state)) return op
    if (op.context !== next.context || op.namespace !== next.namespace || op.cluster !== next.cluster) return op
    if (!(CONFLICTS[op.kind] ?? []).includes(next.kind)) return op
    return { ...op, state: 'superseded', detail: `Superseded by ${next.label}`, finishedAt: next.startedAt }
  })
}

// ---------------------------------------------------------------------------
// Evidence helpers

function haInstance(obs: CNPGObservation, pod: string) {
  return obs.ha?.pods.state === 'ok' ? obs.ha.instances.find((i) => i.pod === pod) : undefined
}

function runtimeInstance(obs: CNPGObservation, pod: string) {
  return obs.runtime?.instances.find((i) => i.pod === pod)
}

/**
 * Whether `pod` restarted after `since`: the Pod was recreated, its postgres
 * container started again, or PostgreSQL's own start time moved (an in-place
 * restart keeps the container). null when none of those is readable.
 */
export function restartedSince(obs: CNPGObservation, pod: string, since: number, baselineUID?: string): boolean | null {
  let known = false
  const h = haInstance(obs, pod)
  if (h) {
    known = true
    if (baselineUID && h.podUID !== baselineUID) return true
    if (h.podCreatedAt && Date.parse(h.podCreatedAt) > since) return true
    if (h.postgresStartedAt && Date.parse(h.postgresStartedAt) > since) return true
  } else if (obs.ha?.pods.state === 'ok') {
    known = true
  }
  const r = runtimeInstance(obs, pod)
  const pm = r?.metrics.postmasterStartTime
  if (pm !== undefined) {
    known = true
    if (pm * 1000 > since) return true
  }
  return known ? false : null
}

function readyNow(obs: CNPGObservation, pod: string): boolean | null {
  const h = haInstance(obs, pod)
  if (h) return h.ready
  const f = obs.facts?.instances.find((i) => i.pod === pod)
  return f && f.podReadable ? f.ready : null
}

/**
 * Whether `pod` streams from the primary, from its own report or the
 * primary's pg_stat_replication. With the proxy allowed, an instance that does
 * not answer yet (restarting) is "not yet"; without it the answer is unknown.
 */
function streamingStandby(obs: CNPGObservation, pod: string): boolean | null {
  if (!obs.runtime || obs.runtime.permission.proxy === 'denied') return null
  const own = runtimeInstance(obs, pod)?.status
  if (own && (own.state === 'ok' || own.state === 'partial') && own.roleDetail) return own.roleDetail === 'streaming'
  const primaryName = obs.facts?.currentPrimary
  const primary = obs.runtime.instances.find((i) => (primaryName ? i.pod === primaryName : i.role === 'primary'))?.status
  if (primary && (primary.state === 'ok' || primary.state === 'partial')) {
    if (!primary.replication) return null
    return primary.replication.some((r) => r.applicationName === pod && r.state === 'streaming')
  }
  return own?.state === 'denied' ? null : false
}

function condition(cluster: any, type: string): { status?: string; reason?: string; message?: string } | undefined {
  return (cluster?.status?.conditions ?? []).find((c: any) => c?.type === type)
}

/**
 * Completed only when every step is seen done. A step Radar cannot see keeps
 * the operation unobservable rather than completed — or stalled.
 */
export function summarizeSteps(steps: CNPGOpStep[]): CNPGObserverResult['state'] {
  const done = steps.filter((s) => s.done === true).length
  const pending = steps.filter((s) => s.done === false).length
  if (done === steps.length) return 'completed'
  if (pending > 0) return done > 0 ? 'progressing' : 'requested'
  return 'unobservable'
}

function key(steps: CNPGOpStep[], extra = ''): string {
  return steps.map((s) => (s.done === true ? '1' : s.done === false ? '0' : '?')).join('') + extra
}

// ---------------------------------------------------------------------------
// Built-in observers

registerCNPGOperationObserver('switchover', (op, obs) => {
  const target = op.target?.name ?? ''
  const oldPrimary = String(op.baseline.currentPrimary ?? '')
  const f = obs.facts
  if (!f) return { state: 'unobservable', detail: 'The Cluster is not readable' }
  if (f.currentPrimary && f.currentPrimary !== target && f.currentPrimary !== oldPrimary) {
    return { state: 'failed', detail: `${f.currentPrimary} became primary instead of ${target}` }
  }
  const promoted = f.currentPrimary === target
  const requested = promoted || f.targetPrimary === target
  if (!requested && f.targetPrimary === oldPrimary && f.phase === 'Cluster in healthy state' && obs.now - op.startedAt > 60_000) {
    return { state: 'failed', detail: `The operator kept ${oldPrimary} as primary${f.phaseReason ? `: ${f.phaseReason}` : ''}` }
  }
  const rejoined = promoted ? streamingStandby(obs, oldPrimary) : false
  const rw = obs.ha?.rwEndpoints
  const steps: CNPGOpStep[] = [
    { label: `Operator accepted ${target} as target`, done: requested },
    { label: `${target} is the primary`, done: promoted },
    { label: `${oldPrimary} is back as a streaming standby`, done: rejoined },
  ]
  // Clients reach the new primary only through the read-write Service; when
  // its endpoints are not readable that step stays unverified, so the
  // switchover can end "unobservable" but never "completed".
  const rwReadable = rw?.state === 'ok'
  steps.push({
    label: rwReadable ? `Read-write Service points at ${target}` : `Read-write Service points at ${target} (endpoints not readable)`,
    done: rwReadable ? promoted && rw.pods.includes(target) && !rw.pods.includes(oldPrimary) : null,
  })
  steps.push({ label: 'Cluster reports a healthy state again', done: promoted && f.phase === 'Cluster in healthy state' })
  let state = summarizeSteps(steps)
  if (requested && !promoted) state = 'observed'
  const rwUnverified = `the read-write Service is unverified${rw?.grant ? ` (needs ${formatGrant(rw.grant)})` : rw?.reason ? ` (${rw.reason})` : ''}`
  const detail =
    rejoined === null && promoted
      ? `Primary changed; whether ${oldPrimary} rejoined needs runtime access (get pods/proxy)${rwReadable ? '' : `; ${rwUnverified}`}`
      : f.phase && f.phase !== 'Cluster in healthy state'
        ? f.phase + (f.phaseReason ? `: ${f.phaseReason}` : '')
        : !rwReadable && promoted
          ? `Primary changed; ${rwUnverified}`
          : undefined
  return { state, steps, detail, progressKey: key(steps, f.phase ?? '') }
})

function restartObserver(podsOf: (op: CNPGTrackedOperation, obs: CNPGObservation) => string[]): CNPGOperationObserver {
  return (op, obs) => {
    const pods = podsOf(op, obs)
    if (pods.length === 0) return { state: 'unobservable', detail: 'No instances to follow' }
    const uids = (op.baseline.podUIDs ?? {}) as Record<string, string>
    const steps: CNPGOpStep[] = pods.map((p) => {
      const restarted = restartedSince(obs, p, op.startedAt, uids[p])
      const ready = restarted ? readyNow(obs, p) : false
      return { label: `${p} restarted and ready`, done: restarted === null ? null : restarted ? ready : false }
    })
    const done = steps.filter((s) => s.done).length
    const state = summarizeSteps(steps)
    const detail =
      state === 'unobservable'
        ? 'Restart or readiness evidence is not readable with your access'
        : `${done} of ${pods.length} restarted${obs.facts?.phase && obs.facts.phase !== 'Cluster in healthy state' ? ` · ${obs.facts.phase}` : ''}`
    return { state, steps, detail, progressKey: key(steps) }
  }
}

registerCNPGOperationObserver(
  'restart',
  restartObserver((op, obs) => {
    const fenced = new Set(obs.facts?.fencedInstances.instances ?? [])
    return ((op.baseline.instances as string[]) ?? []).filter((p) => !fenced.has(p) && !obs.facts?.fencedInstances.all)
  }),
)
registerCNPGOperationObserver(
  'restartInstance',
  restartObserver((op) => (op.target ? [op.target.name] : [])),
)

registerCNPGOperationObserver('destroyInstance', (op, obs) => {
  const f = obs.facts
  if (!f) return { state: 'unobservable', detail: 'The Cluster is not readable' }
  const destroyed = op.target?.name ?? ''
  const before = new Set((op.baseline.instances as string[]) ?? [])
  const gone = !f.instances.some((i) => i.pod === destroyed && (!op.target?.uid || i.podUID === op.target.uid))
  const replacement = f.instances.find((i) => !before.has(i.pod))
  const replacementReady = replacement ? readyNow(obs, replacement.pod) : false
  const steps: CNPGOpStep[] = [
    { label: `${destroyed} removed`, done: gone },
    { label: replacement ? `Replacement ${replacement.pod} ready` : 'Operator created a replacement instance', done: replacement ? replacementReady : false },
    { label: replacement ? `${replacement.pod} streaming from the primary` : 'Replacement streaming from the primary', done: replacement && replacementReady ? streamingStandby(obs, replacement.pod) : false },
    { label: 'Cluster reports a healthy state again', done: gone && !!replacement && f.phase === 'Cluster in healthy state' },
  ]
  const detail = f.phase && f.phase !== 'Cluster in healthy state' ? f.phase + (f.phaseReason ? `: ${f.phaseReason}` : '') : undefined
  return { state: summarizeSteps(steps), steps, detail, progressKey: key(steps, `${replacement?.pod ?? ''}${f.phase ?? ''}`) }
})

registerCNPGOperationObserver('reload', () => ({
  state: 'unobservable',
  final: true,
  detail: 'Requested. Nothing in the cluster reports when a configuration reload completes; check the instance logs for "received SIGHUP".',
}))

// CloudNativePG reports no shutdown signal for a fenced instance: /pg/status
// never carries isFenced, and a masked status error (mightBeUnavailable) also
// arrives without WAL positions. So the fence being recorded is the last thing
// Radar can verify; the shutdown itself stays unverified.
registerCNPGOperationObserver('fence', (op, obs) => {
  const pods = ((op.baseline.instances as string[]) ?? [])
  const f = obs.facts
  const recorded = (p: string): boolean | null => (f ? f.fencedInstances.all || f.fencedInstances.instances.includes(p) : null)
  const steps: CNPGOpStep[] = pods.flatMap((p) => [
    { label: `${p}: fence recorded on the Cluster`, done: recorded(p) },
    { label: `${p}: PostgreSQL stopped (CloudNativePG reports no signal for this)`, done: null },
  ])
  const allRecorded = pods.length > 0 && pods.every((p) => recorded(p) === true)
  return {
    state: allRecorded ? 'unobservable' : summarizeSteps(steps.filter((_, i) => i % 2 === 0)),
    steps,
    final: allRecorded,
    detail: allRecorded
      ? 'Fence recorded. The operator stops PostgreSQL on a fenced instance, but nothing in the cluster confirms the shutdown; the instance logs show it.'
      : 'Waiting for the fence to be recorded on the Cluster',
    progressKey: key(steps),
  }
})

registerCNPGOperationObserver('unfence', (op, obs) => {
  const pods = ((op.baseline.instances as string[]) ?? [])
  const primary = obs.facts?.currentPrimary
  const steps: CNPGOpStep[] = pods.flatMap((p) => {
    const ready = readyNow(obs, p)
    const out: CNPGOpStep[] = [{ label: `${p}: ready again`, done: ready }]
    if (p !== primary) out.push({ label: `${p}: streaming from the primary`, done: ready ? streamingStandby(obs, p) : false })
    return out
  })
  return { state: summarizeSteps(steps), steps, progressKey: key(steps) }
})

registerCNPGOperationObserver('hibernate', (_op, obs) => {
  const c = condition(obs.cluster, 'cnpg.io/hibernation')
  if (!obs.cluster) return { state: 'unobservable', detail: 'The Cluster is not readable' }
  const steps: CNPGOpStep[] = [
    { label: 'Hibernation condition true', done: c?.status === 'True' },
    { label: 'Instance Pods removed', done: obs.ha?.pods.state === 'ok' ? obs.ha.instances.length === 0 : null },
  ]
  return { state: summarizeSteps(steps), steps, detail: c?.message, progressKey: key(steps, c?.reason ?? '') }
})

registerCNPGOperationObserver('rehydrate', (_op, obs) => {
  const c = condition(obs.cluster, 'cnpg.io/hibernation')
  if (!obs.cluster) return { state: 'unobservable', detail: 'The Cluster is not readable' }
  const desired = obs.cluster?.spec?.instances
  const ready = obs.cluster?.status?.readyInstances ?? 0
  const steps: CNPGOpStep[] = [
    { label: 'Hibernation condition cleared', done: !c || c.status !== 'True' },
    { label: `All ${desired ?? ''} instances ready`.replace('  ', ' '), done: typeof desired === 'number' ? ready >= desired : null },
  ]
  return { state: summarizeSteps(steps), steps, detail: `${ready}/${desired ?? '?'} ready`, progressKey: key(steps, String(ready)) }
})

function backupObserver(op: CNPGTrackedOperation, obs: CNPGObservation): CNPGObserverResult {
  if (!op.clusterUID) return { state: 'unobservable', detail: 'The source Cluster identity was not recorded for this backup' }
  const name = op.target?.name
  if (!name) return { state: 'unobservable', detail: 'No Backup name recorded' }
  if (!obs.backups) return { state: 'unobservable', detail: 'Backups are not readable with your access' }
  const b = obs.backups.find((x) => x?.metadata?.name === name && x?.metadata?.namespace === op.namespace)
  if (!b) {
    return obs.now - op.startedAt > 60_000
      ? { state: 'unobservable', detail: `Backup ${name} is not visible (deleted, or outside the backups Radar keeps)`, progressKey: 'absent' }
      : { state: 'requested', detail: `Waiting for Backup ${name} to appear`, progressKey: 'absent' }
  }
  const phase: string | undefined = b.status?.phase
  switch (phase) {
    case 'completed':
      return { state: 'completed', detail: `Backup ${name} completed` }
    case 'failed':
    case 'walArchivingFailing':
      return { state: 'failed', detail: `Backup ${name} ${phase}${b.status?.error ? `: ${b.status.error}` : ''}` }
    case undefined:
    case '':
      return { state: 'observed', detail: `Backup ${name} created; the operator has not picked it up yet`, progressKey: 'new' }
    default:
      return { state: 'progressing', detail: `Backup ${name}: ${phase}`, progressKey: phase }
  }
}
registerCNPGOperationObserver('backup', backupObserver)
registerCNPGOperationObserver('run', backupObserver)

function maintenanceObserver(want: boolean): CNPGOperationObserver {
  return (_op, obs) => {
    const m = obs.facts?.maintenance ?? obs.ha?.maintenance
    if (!m) return { state: 'unobservable', detail: 'The Cluster is not readable' }
    return m.inProgress === want
      ? { state: 'completed', detail: want ? 'Maintenance window is in progress' : 'Maintenance window lifted' }
      : { state: 'requested', detail: 'Waiting for the Cluster spec to reflect it', progressKey: String(m.inProgress) }
  }
}
registerCNPGOperationObserver('setMaintenance', maintenanceObserver(true))
registerCNPGOperationObserver('unsetMaintenance', maintenanceObserver(false))
