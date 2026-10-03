// The client half of the reviewed-action contract shared by workspace
// integrations (server: internal/server/actions.go). A capability says
// whether an action is offered and why not; a request carries the context,
// object UID and facts the user confirmed, and the server refuses it with a
// stable code when any of them no longer holds.

import { formatGrant, type Grant } from '@skyhook-io/k8s-ui'
import { ApiError } from './client'

export interface ActionCapability {
  allowed: boolean
  reason?: string
  /** `unknown`: the permission check itself failed; the apiserver decides on submit. */
  permission: 'allowed' | 'denied' | 'unknown'
  grant?: Grant
}

export interface ActionRequest {
  reviewedContext: string
  uid: string
  facts: Record<string, unknown>
  params?: Record<string, unknown>
}

/**
 * Refusals every action family shares. An integration adds its own codes by
 * widening the type parameter of actionErrorCode.
 */
export type ActionErrorCode = 'context_changed' | 'changed' | 'blocked' | 'partial' | 'outcome_unknown'

// A request that timed out or lost its connection may have been applied by the
// apiserver anyway. 503 without a code is Radar refusing before any write.
const AMBIGUOUS_STATUSES = new Set([500, 502, 504])

export function actionErrorCode<C extends string = ActionErrorCode>(err: unknown): C | ActionErrorCode | undefined {
  if (!err) return undefined
  if (!(err instanceof ApiError)) return 'outcome_unknown'
  const code = err.data?.code
  if (typeof code === 'string') return code as C
  return AMBIGUOUS_STATUSES.has(err.status) ? 'outcome_unknown' : undefined
}

/**
 * Confirm stays locked when the last attempt may have taken effect (unknown)
 * or partly did (partial): repeating it would act on a target that moved.
 */
export function actionOutcomeLocked(err: unknown): boolean {
  const code = actionErrorCode(err)
  return code === 'outcome_unknown' || code === 'partial'
}

/** The mutations a `partial` refusal reports as already done. */
export function actionCompleted(err: unknown): string[] {
  if (!(err instanceof ApiError) || err.data?.code !== 'partial') return []
  const done = err.data.completed
  return Array.isArray(done) ? done.filter((d): d is string => typeof d === 'string') : []
}

/** Why an action is not offered, or undefined when it is. */
export function capabilityReason(cap: ActionCapability): string | undefined {
  if (cap.allowed) return undefined
  if (cap.reason) return cap.reason
  return cap.permission === 'denied' ? `Your account may not do this (${formatGrant(cap.grant) ?? 'permission denied'})` : 'Not available'
}
