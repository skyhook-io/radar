import { ApiError, type CloudInstallAttempted, type CloudInstallBlocked } from '../api/client'

// The Cloud dialog's links into the Hub. utm_content names the link that was
// clicked. After an in-app connect attempt that did not end connected, the
// link also carries radar_outcome, a short phrase for what happened, so the
// wizard opens knowing what the person is coming from. The person sees both
// in the address bar, so every value reads as plain words about the flow —
// never an error message, never a cluster name. Anything that is not a short
// snake_case phrase is dropped here and again on the Hub.
export const SIGNUP_QUERY = '?utm_source=radar-oss&utm_medium=app&utm_campaign=cloud-modal'

// What the person is coming from, whether the pitch should offer "Try
// again" (a failure can be retried; a refusal or the person's own cancel
// cannot be "tried again" without misreading it as something going wrong),
// and, for a blocked plan, the release Radar found so the card's install
// link adopts it instead of opening a fresh install over it.
export interface Handoff {
  outcome: string
  retryable: boolean
  // What Radar attempted, when it got that far — the blocked card's evidence
  // and the input to the exit it offers.
  target?: CloudInstallAttempted | null
  // A prepare error's message, shown on the pitch so the reason outlives the
  // toast that first carried it.
  detail?: string
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
  return { outcome, retryable: true, detail: err instanceof Error ? err.message : undefined }
}

// A blocked plan was refused for a stated reason and trying again reproduces
// it; the wizard is the next step, not a retry.
const BLOCKED_OUTCOMES: Record<CloudInstallBlocked['reason'], string> = {
  gitops: 'blocked_gitops_managed_install',
  preflight: 'blocked_preflight_checks_failed',
  unsupported: 'blocked_unsupported_install',
}

export function handoffForBlocked(reason: CloudInstallBlocked['reason'], target?: CloudInstallAttempted | null): Handoff {
  return { outcome: BLOCKED_OUTCOMES[reason], retryable: false, target: target ?? null }
}

export function signupUrlFor(appUrl: string, content: string, handoff?: Handoff | null): string {
  const url = `${appUrl}/signup${SIGNUP_QUERY}&utm_content=${content}`
  return handoff && isHandoffOutcome(handoff.outcome) ? `${url}&radar_outcome=${handoff.outcome}` : url
}

// The one exit every blocked card offers. An install-page deep link is
// justified only where Radar established both the target and the operation:
// a release it found (adopt, or its GitOps owner's values patch), or a fresh
// install from a complete plan whose discovery saw the whole cluster. The
// Hub's fresh command would reset an existing release's values and its script
// writes the token Secret before Helm runs, so a guessed target is never
// linked. Everywhere else the exit is Radar Cloud itself — the same
// signup/login entry as the pitch — and the copy hands the work to an admin.
// Signed out, the Hub stashes an /install link across sign-in only when it
// carries a target or a method, so every install link carries one.
export interface BlockedExit {
  href: string
  label: string
  // True when href is the install page for an established target.
  install: boolean
}

export function exitFor(appUrl: string, content: string, handoff: Handoff | null | undefined): BlockedExit {
  const generic = { href: signupUrlFor(appUrl, content, handoff), label: 'Open Radar Cloud', install: false }
  const t = handoff?.target
  if (!t) return generic
  // An unsupported refusal names its own remedy (several Radars to pick from,
  // a pairing to recover, ownership Radar will not guess at); a release it
  // happens to know is not an invitation to install over it.
  if (handoff?.outcome === BLOCKED_OUTCOMES.unsupported) return generic
  const params = new URLSearchParams()
  switch (t.mode) {
    case 'adopt':
      params.set('existing', '1')
      params.set('ns', t.namespace)
      params.set('release', t.release)
      params.set('method', 'helm')
      return { href: installHref(appUrl, content, handoff, params), label: 'Get the install command', install: true }
    case 'gitops':
      if (t.method !== 'argocd' && t.method !== 'flux') return generic
      params.set('existing', '1')
      params.set('ns', t.namespace)
      params.set('release', t.release)
      params.set('method', t.method)
      return {
        href: installHref(appUrl, content, handoff, params),
        label: `Get the ${t.method === 'argocd' ? 'Argo CD' : 'Flux'} values patch`,
        install: true,
      }
    default:
      if (t.partialScan) return generic
      params.set('method', 'helm')
      return { href: installHref(appUrl, content, handoff, params), label: 'Get the install command', install: true }
  }
}

function installHref(appUrl: string, content: string, handoff: Handoff | null | undefined, params: URLSearchParams): string {
  const url = `${appUrl}/install?${params.toString()}&${SIGNUP_QUERY.slice(1)}&utm_content=${content}`
  return handoff && isHandoffOutcome(handoff.outcome) ? `${url}&radar_outcome=${handoff.outcome}` : url
}
