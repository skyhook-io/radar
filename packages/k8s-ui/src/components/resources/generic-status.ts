import type { HealthLevel } from './resource-utils'
import { getGatewayPolicyStatus, hasGatewayPolicyStatus } from './gateway-policy-status'

/**
 * Derives a status for a resource with no dedicated renderer — a CRD Radar
 * hasn't curated. Shared by the table cell, the column filter and sort, the
 * drawer dispatch and the generic renderer, so a row cannot show one status
 * and its filter dropdown offer another.
 *
 * Two rules it enforces:
 *   - Polarity is only claimed for a fixed set of condition types. Any other
 *     type is reported as text with an `unknown` tone —
 *     `ConfigurationCreateSuccess=True` is informative, but reading health out
 *     of an arbitrary condition name is a guess.
 *   - `Unknown` is not `False`. A condition the controller can't evaluate is
 *     not a failure.
 *
 * The Go summarizer behind MCP (pkg/ai/context/summary_crd.go) derives this
 * separately and does not agree: it is conditions-first where this is
 * phase-first, and reads `Unknown` as false. They differ on, for example,
 * {phase: Running, Ready: False}.
 */
export interface GenericStatus {
  /** Display text. Never empty when a GenericStatus is returned. */
  text: string
  tone: HealthLevel
  /** Condition message, when one explains a non-healthy tone. */
  reason?: string
}

// Union of the phase vocabularies the five call sites had accumulated.
const HEALTHY_PHASES = new Set([
  'Running', 'Active', 'Succeeded', 'Ready', 'Healthy', 'Available', 'Bound', 'Complete', 'Completed',
])
const DEGRADED_PHASES = new Set([
  'Pending', 'Progressing', 'Unknown', 'Terminating', 'Waiting', 'Provisioning', 'Reconciling',
])
const UNHEALTHY_PHASES = new Set([
  'Failed', 'Error', 'CrashLoopBackOff', 'ImagePullBackOff', 'ErrImagePull', 'Lost', 'Rejected',
])

/**
 * Condition types whose polarity is a convention rather than a guess, most
 * specific first. NOT the Go summarizer's order — it resolves Ready >
 * Available > Synced and knows neither Healthy nor Health; see the note above
 * on why the two are not yet aligned. `Progressing` is deliberately absent:
 * `Progressing=False` on a settled resource is normal, so treating it like a
 * failed `Ready` mislabels healthy objects.
 */
export const KNOWN_POSITIVE_CONDITIONS = ['Ready', 'Available', 'Healthy', 'Health', 'Synced'] as const

/** Condition types that mean trouble when True — the mirror of the set above. */
export const KNOWN_NEGATIVE_CONDITIONS = ['Degraded', 'Warning', 'ScalingLimited'] as const

function conditionReason(cond: any): string | undefined {
  const reason = typeof cond?.reason === 'string' ? cond.reason.trim() : ''
  return reason || undefined
}

function conditionMessage(cond: any): string | undefined {
  const message = typeof cond?.message === 'string' ? cond.message.trim() : ''
  return message || undefined
}

function fromCondition(cond: any, polarityKnown: boolean): GenericStatus | null {
  const type = typeof cond?.type === 'string' ? cond.type.trim() : ''
  if (!type) return null
  const state = cond?.status

  if (state === 'True') {
    return { text: type, tone: polarityKnown ? 'healthy' : 'unknown' }
  }
  if (state === 'False') {
    return {
      text: conditionReason(cond) || `Not ${type}`,
      tone: polarityKnown ? 'unhealthy' : 'unknown',
      reason: conditionMessage(cond),
    }
  }
  // "Unknown" (or anything else): the controller could not determine this.
  // Not a failure — report it without a health claim.
  return { text: conditionReason(cond) || type, tone: 'unknown', reason: conditionMessage(cond) }
}

/**
 * Derives a status for a resource with no kind-specific handling.
 * Returns null when the object carries nothing that reads as a status —
 * callers render their own placeholder rather than inventing one.
 */
export function getGenericResourceStatus(resource: any): GenericStatus | null {
  const status = resource?.status
  if (!status || typeof status !== 'object') return null

  // Gateway API policies keep their conditions per-ancestor, one level below
  // where the rest of this function looks. Dispatched on the shape and resolved
  // by its own reader, because the rungs below would answer it wrongly: they
  // take a positive condition before a negative one, and a policy that is
  // `Accepted=True, Programmed=False` has been taken up and then failed.
  // Returns null when the ancestors say nothing, so a policy that also
  // publishes top-level conditions still gets read — GCPBackendPolicy declares
  // both, and suppressing its conditions would hide a Ready=False behind an
  // empty ancestor list.
  if (hasGatewayPolicyStatus(resource)) {
    const policy = getGatewayPolicyStatus(resource)
    if (policy) return policy
  }

  const phase = typeof status.phase === 'string' ? status.phase.trim() : ''
  if (phase) {
    const reason = typeof status.message === 'string' && status.message.trim()
      ? status.message.trim()
      : typeof status.reason === 'string' && status.reason.trim()
        ? status.reason.trim()
        : undefined
    if (HEALTHY_PHASES.has(phase)) return { text: phase, tone: 'healthy' }
    if (DEGRADED_PHASES.has(phase)) return { text: phase, tone: 'degraded', reason }
    if (UNHEALTHY_PHASES.has(phase)) return { text: phase, tone: 'unhealthy', reason }
    // An unrecognized phase does NOT short-circuit. Its health is unknown to
    // us, so a Ready=False condition or a failed replica count below is the
    // better answer; the phase text is kept as the last resort. Returning here
    // would hide a real failure behind an operator's private vocabulary.
  }

  const conditions = Array.isArray(status.conditions) ? status.conditions : []
  for (const type of KNOWN_POSITIVE_CONDITIONS) {
    const match = conditions.find((c: any) => c?.type === type)
    if (match) {
      const derived = fromCondition(match, true)
      if (derived) return derived
    }
  }
  for (const type of KNOWN_NEGATIVE_CONDITIONS) {
    const match = conditions.find((c: any) => c?.type === type && c?.status === 'True')
    if (match) {
      return { text: match.type, tone: 'degraded', reason: conditionMessage(match) ?? conditionReason(match) }
    }
  }

  // Replica counts read as a status on their own, and say more than a vendor
  // condition name would. Ranked above the unknown-polarity fallback for that
  // reason.
  if (typeof status.replicas === 'number' && status.replicas > 0) {
    const desired = status.replicas
    const ready = typeof status.readyReplicas === 'number' ? status.readyReplicas
      : typeof status.availableReplicas === 'number' ? status.availableReplicas
        : 0
    const text = `${ready}/${desired} Ready`
    if (ready >= desired) return { text, tone: 'healthy' }
    return { text, tone: ready > 0 ? 'degraded' : 'unhealthy' }
  }

  // No condition whose polarity we know. Report the first one as text so a CRD
  // that only publishes its own vocabulary still says something, but without a
  // health claim.
  for (const cond of conditions) {
    const derived = fromCondition(cond, false)
    if (derived) return derived
  }

  // `state` and `status` are both used as a bare string status by CRDs that
  // publish neither a phase nor conditions. Guarded on string because both
  // names are also used for nested objects.
  for (const key of ['state', 'status'] as const) {
    const value = status[key]
    if (typeof value === 'string' && value.trim()) {
      return { text: value.trim(), tone: 'unknown' }
    }
  }

  if (phase) return { text: phase, tone: 'unknown' }

  return null
}
