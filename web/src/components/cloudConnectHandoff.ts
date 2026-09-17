import { ApiError, type CloudInstallAttempted, type CloudInstallBlocked, type CloudInstallFailure } from '../api/client'

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

// clusterName is the kubeconfig context, offered to the Hub as the cluster's
// name (`name=`) so the install page opens with its form already filled in;
// the person can still change it there.
export function exitFor(appUrl: string, content: string, handoff: Handoff | null | undefined, clusterName?: string): BlockedExit {
  const generic = { href: signupUrlFor(appUrl, content, handoff), label: 'Open Radar Cloud', install: false }
  const t = handoff?.target
  if (!t) return generic
  // An unsupported refusal names its own remedy (several Radars to pick from,
  // a pairing to recover, ownership Radar will not guess at); a release it
  // happens to know is not an invitation to install over it.
  if (handoff?.outcome === BLOCKED_OUTCOMES.unsupported) return generic
  const params = new URLSearchParams()
  if (clusterName) params.set('name', clusterName)
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

// The note a blocked person hands to whoever administers the cluster. An
// ask, not a diagnostic: the request first, the link second, the evidence
// last — and written about the attempt rather than in the reader's or the
// sender's voice, so it can be pasted as-is or trimmed to a sentence.
export interface AdminNoteContext {
  context?: string
  cluster?: string
}

export function composeAdminNote(blocked: CloudInstallBlocked, exit: BlockedExit, where: AdminNoteContext = {}): string {
  const a = blocked.attempted
  const name = where.context || where.cluster || 'this cluster'
  const identity = blocked.identity ? ` (${blocked.identity})` : ''
  const tool = a?.method === 'argocd' ? 'Argo CD' : a?.method === 'flux' ? 'Flux' : 'a GitOps controller'

  let because: string
  switch (blocked.reason) {
    case 'gitops':
      because = `the install is managed by ${tool}, so connecting it is a values change in the repository — Radar Cloud generates the patch`
      break
    case 'unsupported':
      because = `Radar reported: ${blocked.message.replace(/\s+/g, ' ').trim()}`
      break
    default:
      switch (blocked.cause) {
        case 'permissions':
          because = `the identity in use${identity} doesn't have the permissions to install it`
          break
        case 'verification':
          because = 'the Radar version in use can\'t install this chart version from there'
          break
        default:
          because = 'the cluster refused part of the install — a policy, or something already there (details below)'
      }
  }

  const ask = exit.install
    ? 'Could someone with cluster access connect it?'
    : 'Could someone with cluster access connect it from Radar Cloud?'
  const linkLabel = !exit.install
    ? 'Open Radar Cloud:'
    : blocked.reason === 'gitops'
      ? `Open Radar Cloud to get the values patch for ${tool} (sign in, confirm the cluster name; it also shows the one command that creates the token Secret):`
      : 'Open Radar Cloud to get the install command (sign in, confirm the cluster name, pick Helm / Argo CD / Flux):'

  // Observations, not Radar narrating itself: what was found, where it stopped.
  const details: string[] = []
  if (a) {
    const target = `release ${a.release} in namespace ${a.namespace}`
    const stage =
      a.stage === 'inspect'
        ? "the check stopped while reading Helm's release records"
        : a.stage === 'prepare'
          ? 'the check stopped while preparing the chart'
          : 'the dry run of the install stopped'
    details.push(
      a.mode === 'gitops'
        ? `The ${target} is managed by ${tool}.`
        : a.mode === 'adopt'
          ? `An existing ${target} was found; ${stage}.`
          : a.partialScan
            ? `Only namespace ${a.namespace} could be checked for an existing install; ${stage}.`
            : `No Radar install was found in the cluster; ${stage}.`,
    )
  } else if (blocked.reason === 'preflight') {
    details.push('The check for an existing Radar install did not complete.')
  }
  for (const line of blocked.blocking ?? []) details.push(trimRefusal(line))
  if (a?.releaseUnread) details.push('Please confirm nothing is already installed before a fresh install.')
  if (a?.partialScan) details.push('Please check the rest of the cluster for an existing install first.')

  return [
    `Request to connect cluster ${name} to Radar Cloud`,
    '',
    `Connecting it from Radar was blocked: ${because}. Nothing in the cluster was changed. ${ask}`,
    '',
    linkLabel,
    plainLink(exit.href),
    ...(details.length ? ['', `Details: ${details.join(' ')}`] : []),
  ].join('\n')
}

// The note is text a person reads and forwards, so its link carries only what
// the page needs to open in the right place (an existing release to adopt,
// the install method) plus one short marker saying it arrived by handoff;
// the card's own button keeps the full attribution params. The Hub stashes
// the target and method across sign-in, so the admin still lands on the
// prefilled install page.
export const HANDOFF_VIA = 'admin_handoff'

function plainLink(href: string): string {
  try {
    const url = new URL(href)
    const keep = new URLSearchParams()
    for (const key of ['name', 'existing', 'ns', 'release', 'method']) {
      const v = url.searchParams.get(key)
      if (v) keep.set(key, v)
    }
    keep.set('via', HANDOFF_VIA)
    url.search = keep.toString()
    return url.toString()
  } catch {
    return href
  }
}

// A refusal line is a Go error chain — "create Namespace "radar": namespaces
// "radar" is forbidden: ValidatingAdmissionPolicy … denied request: <reason>".
// Keep what was attempted and why it was refused; drop the wrapping between.
function trimRefusal(line: string): string {
  const parts = line.split(': ').map((p) => p.trim()).filter(Boolean)
  if (parts.length <= 2) return line.trim().replace(/\.?$/, '.')
  return `${parts[0]} — ${parts[parts.length - 1]}`.replace(/\.?$/, '.')
}

// Failures after the Hub approved — Helm ran, or ran and the tunnel never
// came up — are the ones whose guidance is written for an operator: inspect
// commands, "keep this Hub cluster, don't rerun". A person who cannot act
// on that hands it over the same way a blocked one does, as a request to
// finish the connection rather than start one.
export const HANDOFF_FAILURE_KINDS = new Set(['helm_provision_failed', 'installed_but_tunnel_not_confirmed'])

export function needsAdminHandoff(failure: CloudInstallFailure | undefined): boolean {
  return !!failure && HANDOFF_FAILURE_KINDS.has(failure.kind)
}

export function composeFailureNote(failure: CloudInstallFailure, where: AdminNoteContext = {}): string {
  const name = where.context || where.cluster || 'this cluster'
  const g = failure.guidance
  const stage =
    failure.kind === 'installed_but_tunnel_not_confirmed'
      ? 'Radar was installed by Helm, but its connection to Radar Cloud could not be confirmed'
      : 'the Helm install of Radar failed'
  const lines: string[] = [
    `Request to finish connecting cluster ${name} to Radar Cloud`,
    '',
    `Connecting it from Radar got as far as the install: ${stage}. ${failure.message.replace(/\s+/g, ' ').trim()} Could someone with cluster access take it from here?`,
  ]
  if (g?.clusterUrl) lines.push('', 'The cluster in Radar Cloud (its page shows the recovery options):', plainLink(g.clusterUrl))
  const details: string[] = []
  if (g?.summary && g.summary !== failure.message) details.push(g.summary)
  for (const l of g?.lines ?? []) details.push(l)
  if (details.length) lines.push('', `Details: ${details.join(' ')}`)
  if (g?.inspect?.length) lines.push('', 'To inspect:', ...g.inspect.map((c) => `  ${c}`))
  return lines.join('\n')
}
