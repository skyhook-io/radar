import { ApiError, type CloudInstallBlocked } from '../api/client'

// The Cloud dialog's links into the Hub. utm_content names the link that was
// clicked. After an in-app connect attempt that did not end connected, the
// link also carries radar_outcome, a short phrase for what happened, so the
// wizard opens knowing what the person is coming from. The person sees both
// in the address bar, so every value reads as plain words about the flow —
// never an error message, never a cluster name. Anything that is not a short
// snake_case phrase is dropped here and again on the Hub.
export const SIGNUP_QUERY = '?utm_source=radar-oss&utm_medium=app&utm_campaign=cloud-modal'

// What the person is coming from, and whether the pitch should offer "Try
// again": a failure can be retried, a refusal or the person's own cancel
// cannot be "tried again" without misreading it as something going wrong.
export interface Handoff {
  outcome: string
  retryable: boolean
}

// Closed shape, not a closed list: flow failure kinds come from the server
// (hub_connect_request_failed, approval_window_expired, helm_provision_failed,
// …) and this frontend must not need a release to forward a new one.
const OUTCOME_SHAPE = /^[a-z][a-z_]{0,39}$/

export function isHandoffOutcome(value: unknown): value is string {
  return typeof value === 'string' && OUTCOME_SHAPE.test(value)
}

// The prepare POST failed before any flow existed, so there is no failure
// kind to forward; classify by HTTP class. Prepare never contacts the Hub: a
// 503 is Radar without a cluster (disconnected or still connecting), any other
// status is Radar itself declining or failing the inspection, a TypeError is
// fetch failing to reach Radar's own server, and anything else is a response
// that could not be used. All of them are worth trying again.
export function handoffForPrepareError(err: unknown): Handoff {
  let outcome = 'cluster_inspect_request_error'
  if (err instanceof ApiError) {
    outcome = err.status === 503 ? 'radar_not_connected_to_cluster' : 'cluster_inspect_failed'
  } else if (err instanceof TypeError) {
    outcome = 'radar_server_unreachable'
  }
  return { outcome, retryable: true }
}

// A blocked plan was refused for a stated reason and trying again reproduces
// it; the wizard is the next step, not a retry.
const BLOCKED_OUTCOMES: Record<CloudInstallBlocked['reason'], string> = {
  gitops: 'blocked_gitops_managed_install',
  preflight: 'blocked_preflight_checks_failed',
  unsupported: 'blocked_unsupported_install',
}

export function handoffForBlocked(reason: CloudInstallBlocked['reason']): Handoff {
  return { outcome: BLOCKED_OUTCOMES[reason], retryable: false }
}

export function signupUrlFor(appUrl: string, content: string, handoff?: Handoff | null): string {
  const url = `${appUrl}/signup${SIGNUP_QUERY}&utm_content=${content}`
  return handoff && isHandoffOutcome(handoff.outcome) ? `${url}&radar_outcome=${handoff.outcome}` : url
}
